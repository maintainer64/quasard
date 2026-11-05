#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-only
"""Build-time conversion of pinned GigaAM v3_e2e_rnnt for the native Go backend.

Install native/requirements-gigaam-export.txt into an isolated Python environment.
This tool is not used by the server/runtime image. No user audio is packaged.
"""

import argparse
import gc
import hashlib
import importlib.metadata
import json
from pathlib import Path
import shutil
import uuid

import gigaam
import torch
from sentencepiece import sentencepiece_model_pb2


def fingerprint(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(4 * 1024 * 1024), b""):
            digest.update(chunk)
    return {"bytes": path.stat().st_size, "sha256": digest.hexdigest()}



MODEL_REPOSITORY = "ai-sage/GigaAM-v3"
MODEL_REVISION = "7655ad717f8122257385bb4b2f373db3697e8680"
MODEL_SHA256 = "afc6dcbae8320ea56f2cddebc0f13fbf62c9d59b6ddcad899782623c8610826a"
TOKENIZER_SHA256 = "828c12c991019eef952a960661f25a92d6ad279591e2ea466b4aeddf1d20a18a"


def load_source(download_root):
    # The official CDN has an expired TLS certificate on some routes. Use the
    # publisher's immutable HTTPS mirror, never disable certificate validation.
    # HF's wrapper prefix differs, but all 561 tensors were checked bit-for-bit
    # against the original v3_e2e_rnnt checkpoint before pinning this revision.
    from huggingface_hub import hf_hub_download
    from omegaconf import OmegaConf

    def get(name):
        return Path(hf_hub_download(MODEL_REPOSITORY, name, revision=MODEL_REVISION,
                                   cache_dir=str(download_root)))

    weights, tokenizer, config_file = get("pytorch_model.bin"), get("tokenizer.model"), get("config.json")
    if fingerprint(weights)["sha256"] != MODEL_SHA256 or fingerprint(tokenizer)["sha256"] != TOKENIZER_SHA256:
        raise ValueError("Official GigaAM mirror checksum mismatch")
    config = json.loads(config_file.read_text())["cfg"]["model"]["cfg"]
    targets = {"modeling_gigaam.FeatureExtractor": "gigaam.preprocess.FeatureExtractor",
               "modeling_gigaam.ConformerEncoder": "gigaam.encoder.ConformerEncoder",
               "modeling_gigaam.RNNTHead": "gigaam.decoder.RNNTHead",
               "modeling_gigaam.RNNTGreedyDecoding": "gigaam.decoding.RNNTGreedyDecoding"}

    def local_targets(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "_target_":
                    if item not in targets:
                        raise ValueError("Unexpected GigaAM configuration target")
                    value[key] = targets[item]
                else:
                    local_targets(item)
        elif isinstance(value, list):
            for item in value:
                local_targets(item)

    local_targets(config)
    config["decoding"]["model_path"] = str(tokenizer)
    config["encoder"]["flash_attn"] = False
    state = torch.load(weights, map_location="cpu", weights_only=True)
    if not all(key.startswith("model.") for key in state):
        raise ValueError("Unexpected official GigaAM state dictionary")
    model = gigaam.GigaAMASR(OmegaConf.create(config))
    model.load_state_dict({key.removeprefix("model."): value for key, value in state.items()}, strict=True)
    del state
    return model.eval().float().to("cpu"), weights, tokenizer


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--download-root", type=Path, default=Path.home() / ".cache" / "gigaam")
    parser.add_argument("--reuse-export", type=Path, help="Previously exported official ONNX graphs")
    parser.add_argument("--quantization", choices=("float32", "int8_encoder"), default="int8_encoder")
    args = parser.parse_args()
    output = args.output.absolute()
    if output.exists():
        parser.error("Output exists; choose a new output directory")
    torch.set_num_threads(2)
    model, source_weights, source_tokenizer = load_source(args.download_root)
    cfg = model.cfg
    frontend = model.preprocessor.featurizer[0]
    expected = (frontend.n_fft, frontend.win_length, frontend.hop_length,
                frontend.spectrogram.center, frontend.power, frontend.mel_scale.n_mels)
    if expected != (320, 320, 160, False, 2.0, 64):
        raise ValueError(f"Unsupported frontend: {expected}")
    if torch.count_nonzero(model.head.decoder.embed.weight[1024]).item():
        raise ValueError("Expected a zero RNNT blank embedding")
    temporary = output.with_name(output.name + ".partial-" + uuid.uuid4().hex[:8])
    temporary.mkdir(parents=True)
    names = {"encoder.onnx": "v3_e2e_rnnt_encoder.onnx",
             "decoder.onnx": "v3_e2e_rnnt_decoder.onnx",
             "joiner.onnx": "v3_e2e_rnnt_joint.onnx"}
    try:
        if args.reuse_export:
            for name in names.values():
                shutil.copyfile(args.reuse_export / name, temporary / name)
        else:
            model.to_onnx(str(temporary), dtype=torch.float32)
        (temporary / "v3_e2e_rnnt.yaml").unlink(missing_ok=True)
        for name, original in names.items():
            (temporary / original).rename(temporary / name)
        tokenizer = model.decoding.tokenizer.model
        tokens = []
        for i in range(tokenizer.get_piece_size()):
            kind = ("unknown" if tokenizer.is_unknown(i) else "control" if tokenizer.is_control(i)
                    else "byte" if tokenizer.is_byte(i) else "normal")
            tokens.append({"piece": tokenizer.id_to_piece(i), "kind": kind})
        proto = sentencepiece_model_pb2.ModelProto()
        proto.ParseFromString(source_tokenizer.read_bytes())
        manifest = {"schema_version": 1, "model": "v3_e2e_rnnt", "sample_rate": 16000,
                    "fft_size": 320, "hop_size": 160, "mel_bins": 64, "center": False,
                    "mel_filters": frontend.mel_scale.fb.T.contiguous().flatten().tolist(),
                    "encoder_dim": 768, "hidden_size": 320, "layers": 1, "blank_id": 1024,
                    "max_symbols": model.decoding.max_symbols,
                    "quantization": args.quantization,
                    "unknown_surface": proto.trainer_spec.unk_surface, "tokens": tokens,
                    "files": {},
                    "source": {"repository": "https://github.com/salute-developers/GigaAM",
                               "revision": "7447938d791c4f3e643386ee22c33777004293a5",
                               "weights": fingerprint(source_weights),
                               "model_repository": MODEL_REPOSITORY, "model_revision": MODEL_REVISION,
                               "tokenizer": fingerprint(source_tokenizer),
                               "versions": {p: importlib.metadata.version(p) for p in
                                            ("gigaam", "torch", "torchaudio", "onnx", "onnxruntime")}}}
        # Exporting and quantising need separate large buffers. Release the
        # resident torch encoder before ORT builds its quantised graph in CI.
        del model, frontend, tokenizer
        gc.collect()
        if args.quantization == "int8_encoder":
            from onnxruntime.quantization import QuantType, quantize_dynamic

            quantized = temporary / "encoder-int8.onnx"
            quantize_dynamic(str(temporary / "encoder.onnx"), str(quantized),
                             weight_type=QuantType.QInt8, per_channel=True,
                             op_types_to_quantize=["MatMul", "Gemm"],
                             extra_options={"MatMulConstBOnly": True})
            quantized.replace(temporary / "encoder.onnx")
        manifest["files"] = {name: fingerprint(temporary / name) for name in names}
        (temporary / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
        distribution = importlib.metadata.distribution("gigaam")
        license_path = next((distribution.locate_file(p) for p in distribution.files
                             if p.name == "LICENSE" and "dist-info" in str(p)), None)
        if license_path is None:
            raise ValueError("GigaAM distribution is missing its MIT license")
        shutil.copyfile(license_path, temporary / "LICENSE.GigaAM")
        temporary.rename(output)
    except BaseException:
        shutil.rmtree(temporary, ignore_errors=True)
        raise
    print(f"GigaAM native model ready: {output}")


if __name__ == "__main__":
    main()
