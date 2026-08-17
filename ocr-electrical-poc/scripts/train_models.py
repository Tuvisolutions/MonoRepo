#!/usr/bin/env python3
"""Train / export multiple OCR model backends from the labeled SW dataset.

Backends:
  gemini-jsonl   Build Gemini/Vertex multimodal JSONL (always safe/local)
  gemini-tune    Start a Gemini supervised fine-tune job via google-genai (needs API)
  trocr          Fine-tune HuggingFace TrOCR on raw_ocr_text (needs torch + transformers)
  evaluate       Score current GEMINI_MODEL against gold labels (NMI exact match)

Example:
  python scripts/train_models.py --dataset dataset/sw_prod \\
    --backends gemini-jsonl,evaluate

  python scripts/train_models.py --dataset dataset/sw_prod \\
    --backends gemini-jsonl,gemini-tune --base-model gemini-2.0-flash
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Optional

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from dotenv import load_dotenv


def paired_examples(dataset: Path) -> list[tuple[Path, Path, dict[str, Any]]]:
    images = dataset / "images"
    labels = dataset / "labels"
    pairs: list[tuple[Path, Path, dict[str, Any]]] = []
    for label_path in sorted(labels.glob("*.json")):
        stem = label_path.stem
        image_path = None
        for ext in (".jpg", ".jpeg", ".png", ".webp"):
            candidate = images / f"{stem}{ext}"
            if candidate.exists():
                image_path = candidate
                break
        if image_path is None:
            continue
        gold = json.loads(label_path.read_text(encoding="utf-8"))
        pairs.append((image_path, label_path, gold))
    return pairs


def backend_gemini_jsonl(dataset: Path, out: Path, prompt: Optional[str]) -> dict[str, Any]:
    cmd = [
        sys.executable,
        str(ROOT / "scripts" / "prepare_finetune_jsonl.py"),
        "--dataset",
        str(dataset),
        "--out",
        str(out),
    ]
    if prompt:
        cmd.extend(["--prompt", prompt])
    subprocess.check_call(cmd)
    lines = sum(1 for _ in out.open(encoding="utf-8"))
    return {"backend": "gemini-jsonl", "path": str(out), "examples": lines}


def backend_gemini_tune(
    dataset: Path,
    *,
    jsonl_path: Path,
    base_model: str,
    display_name: str,
) -> dict[str, Any]:
    """Attempt Gemini supervised fine-tuning via the google-genai tunings API.

    Notes:
    - AI Studio / consumer Gemini keys often cannot create tune jobs.
    - Vertex AI with a GCS-hosted JSONL is the production path.
    - This backend still builds the JSONL and tries the API so failures are explicit.
    """
    from google import genai

    load_dotenv(ROOT / ".env")
    api_key = (os.getenv("GEMINI_API_KEY") or "").strip()
    if not api_key or api_key.startswith("your_"):
        raise RuntimeError("GEMINI_API_KEY required for gemini-tune")

    if not jsonl_path.is_file():
        backend_gemini_jsonl(dataset, jsonl_path, None)

    client = genai.Client(api_key=api_key)
    # Upload training file when the Files API is available.
    uploaded = None
    try:
        uploaded = client.files.upload(file=str(jsonl_path))
    except Exception as exc:  # noqa: BLE001
        return {
            "backend": "gemini-tune",
            "status": "needs_vertex",
            "reason": (
                "Could not upload training JSONL via Files API. "
                "Upload exports/finetune.jsonl to GCS and run Vertex SFT. "
                f"upload_error={exc}"
            ),
            "jsonl": str(jsonl_path),
            "suggested_base_model": base_model,
        }

    try:
        # google-genai tunings surface varies by SDK version; try common shapes.
        tune_fn = getattr(getattr(client, "tunings", None), "tune", None) or getattr(
            getattr(client, "tunings", None), "create", None
        )
        if tune_fn is None:
            return {
                "backend": "gemini-tune",
                "status": "unsupported_sdk",
                "reason": "Installed google-genai has no tunings.tune/create",
                "uploaded_file": getattr(uploaded, "name", None),
                "jsonl": str(jsonl_path),
            }
        job = tune_fn(
            base_model=base_model,
            training_dataset={"examples": {"uri": getattr(uploaded, "uri", None) or uploaded.name}},
            display_name=display_name,
        )
        return {
            "backend": "gemini-tune",
            "status": "started",
            "job": str(job),
            "base_model": base_model,
            "jsonl": str(jsonl_path),
        }
    except Exception as exc:  # noqa: BLE001
        return {
            "backend": "gemini-tune",
            "status": "failed",
            "error": str(exc),
            "jsonl": str(jsonl_path),
            "hint": (
                "Use Vertex AI supervised fine-tuning with a GCS URI for the JSONL. "
                "Consumer API keys usually cannot launch tune jobs."
            ),
        }


def backend_trocr(dataset: Path, out_dir: Path, epochs: int, limit: int) -> dict[str, Any]:
    """Fine-tune microsoft/trocr-base-printed on gold raw_ocr_text."""
    try:
        import torch
        from PIL import Image
        from torch.utils.data import DataLoader, Dataset
        from transformers import (
            TrOCRProcessor,
            VisionEncoderDecoderModel,
            default_data_collator,
        )
    except ImportError as exc:
        return {
            "backend": "trocr",
            "status": "missing_deps",
            "error": str(exc),
            "install": "pip install torch transformers",
        }

    pairs = paired_examples(dataset)
    samples = []
    for image_path, _label_path, gold in pairs:
        text = (gold.get("raw_ocr_text") or "").strip()
        if not text:
            # Fall back to NMI / handwritten labels.
            nmi = (gold.get("identifiers") or {}).get("nmi")
            labels = gold.get("handwritten_labels") or []
            text = " | ".join([*(labels or []), *( [f"NMI {nmi}"] if nmi else [])]).strip()
        if text:
            samples.append((image_path, text))
    if limit:
        samples = samples[:limit]
    if len(samples) < 2:
        return {"backend": "trocr", "status": "too_few_samples", "count": len(samples)}

    processor = TrOCRProcessor.from_pretrained("microsoft/trocr-base-printed")
    model = VisionEncoderDecoderModel.from_pretrained("microsoft/trocr-base-printed")
    device = "cuda" if torch.cuda.is_available() else "cpu"
    model.to(device)

    class OcrDataset(Dataset):
        def __len__(self) -> int:
            return len(samples)

        def __getitem__(self, idx: int) -> dict[str, Any]:
            path, text = samples[idx]
            image = Image.open(path).convert("RGB")
            pixel_values = processor(image, return_tensors="pt").pixel_values.squeeze(0)
            labels = processor.tokenizer(
                text,
                padding="max_length",
                max_length=128,
                truncation=True,
                return_tensors="pt",
            ).input_ids.squeeze(0)
            labels[labels == processor.tokenizer.pad_token_id] = -100
            return {"pixel_values": pixel_values, "labels": labels}

    loader = DataLoader(OcrDataset(), batch_size=1, shuffle=True, collate_fn=default_data_collator)
    optim = torch.optim.AdamW(model.parameters(), lr=5e-5)
    model.train()
    losses: list[float] = []
    for epoch in range(epochs):
        epoch_loss = 0.0
        for batch in loader:
            batch = {k: v.to(device) for k, v in batch.items()}
            out = model(**batch)
            loss = out.loss
            loss.backward()
            optim.step()
            optim.zero_grad()
            epoch_loss += float(loss.item())
        avg = epoch_loss / max(len(loader), 1)
        losses.append(avg)
        print(f"  trocr epoch {epoch + 1}/{epochs} loss={avg:.4f}")

    out_dir.mkdir(parents=True, exist_ok=True)
    model.save_pretrained(out_dir)
    processor.save_pretrained(out_dir)
    return {
        "backend": "trocr",
        "status": "trained",
        "samples": len(samples),
        "epochs": epochs,
        "losses": losses,
        "out_dir": str(out_dir),
        "device": device,
    }


def backend_evaluate(dataset: Path, limit: int) -> dict[str, Any]:
    from app.gemini_client import analyze_image

    load_dotenv(ROOT / ".env")
    pairs = paired_examples(dataset)
    if limit:
        pairs = pairs[:limit]
    if not pairs:
        return {"backend": "evaluate", "status": "no_pairs"}

    nmi_correct = 0
    nmi_total = 0
    errors = 0
    details = []
    for image_path, _label_path, gold in pairs:
        gold_nmi = (gold.get("identifiers") or {}).get("nmi")
        if not gold_nmi:
            continue
        nmi_total += 1
        mime = {
            ".jpg": "image/jpeg",
            ".jpeg": "image/jpeg",
            ".png": "image/png",
            ".webp": "image/webp",
        }.get(image_path.suffix.lower(), "image/jpeg")
        try:
            pred = analyze_image(image_path.read_bytes(), mime_type=mime)
            pred_nmi = pred.identifiers.nmi
            ok = (pred_nmi or "") == gold_nmi
            if ok:
                nmi_correct += 1
            details.append(
                {
                    "image": image_path.name,
                    "gold_nmi": gold_nmi,
                    "pred_nmi": pred_nmi,
                    "match": ok,
                }
            )
        except Exception as exc:  # noqa: BLE001
            errors += 1
            details.append({"image": image_path.name, "error": str(exc)})

    return {
        "backend": "evaluate",
        "status": "done",
        "nmi_exact": nmi_correct,
        "nmi_total": nmi_total,
        "nmi_accuracy": (nmi_correct / nmi_total) if nmi_total else None,
        "errors": errors,
        "details": details[:50],
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", type=Path, default=ROOT / "dataset" / "sw_prod")
    parser.add_argument(
        "--backends",
        default="gemini-jsonl",
        help="Comma list: gemini-jsonl,gemini-tune,trocr,evaluate",
    )
    parser.add_argument("--out-dir", type=Path, default=ROOT / "exports" / "train_runs")
    parser.add_argument("--base-model", default=os.getenv("GEMINI_MODEL", "gemini-2.0-flash"))
    parser.add_argument("--display-name", default="tuvi-electrical-ocr")
    parser.add_argument("--trocr-epochs", type=int, default=2)
    parser.add_argument("--limit", type=int, default=0, help="Limit pairs for trocr/evaluate")
    args = parser.parse_args()

    load_dotenv(ROOT / ".env")
    dataset = args.dataset
    if not (dataset / "labels").is_dir():
        raise SystemExit(f"No labels under {dataset}/labels — run build_labels_from_metadata.py first")

    args.out_dir.mkdir(parents=True, exist_ok=True)
    backends = [b.strip() for b in args.backends.split(",") if b.strip()]
    results: list[dict[str, Any]] = []
    jsonl_path = args.out_dir / "finetune.jsonl"

    for name in backends:
        print(f"== backend: {name} ==")
        if name == "gemini-jsonl":
            results.append(backend_gemini_jsonl(dataset, jsonl_path, None))
        elif name == "gemini-tune":
            results.append(
                backend_gemini_tune(
                    dataset,
                    jsonl_path=jsonl_path,
                    base_model=args.base_model,
                    display_name=args.display_name,
                )
            )
        elif name == "trocr":
            results.append(
                backend_trocr(
                    dataset,
                    args.out_dir / "trocr",
                    epochs=args.trocr_epochs,
                    limit=args.limit,
                )
            )
        elif name == "evaluate":
            results.append(backend_evaluate(dataset, args.limit))
        else:
            results.append({"backend": name, "status": "unknown_backend"})

    report_path = args.out_dir / "train_report.json"
    report_path.write_text(json.dumps(results, indent=2, default=str), encoding="utf-8")
    print(json.dumps(results, indent=2, default=str))
    print(f"Report → {report_path}")


if __name__ == "__main__":
    main()
