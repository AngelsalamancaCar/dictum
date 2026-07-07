"""FastAPI entrypoint for the dictum ML worker."""
import tempfile
from pathlib import Path
from typing import Literal

from fastapi import FastAPI, UploadFile
from pydantic import BaseModel

from parsing.liteparse_service import parse_document
from rag.chunking import chunk_document
from rag.embedding_service import embed_passages, embed_queries

app = FastAPI(title="dictum-ml")


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


class ParseResponse(BaseModel):
    text: str
    pages: list[dict]
    chunks: list[dict]


@app.post("/parse", response_model=ParseResponse)
async def parse(file: UploadFile):
    suffix = Path(file.filename or "").suffix
    data = await file.read()
    with tempfile.NamedTemporaryFile(suffix=suffix, delete=False) as tmp:
        tmp.write(data)
        tmp_path = Path(tmp.name)
    try:
        parsed = parse_document(tmp_path)
    finally:
        tmp_path.unlink(missing_ok=True)

    chunks = chunk_document(parsed["text"])
    return ParseResponse(
        text=parsed["text"],
        pages=parsed["pages"],
        chunks=[{"text": c.text, "section_label": c.section_label} for c in chunks],
    )


class EmbedRequest(BaseModel):
    texts: list[str]
    kind: Literal["passage", "query"] = "passage"


class EmbedResponse(BaseModel):
    embeddings: list[list[float]]
    dimension: int


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest):
    fn = embed_queries if req.kind == "query" else embed_passages
    vectors = fn(req.texts)
    return EmbedResponse(embeddings=vectors.tolist(), dimension=vectors.shape[1])


@app.post("/classify-knn")
def classify_knn():
    raise NotImplementedError


@app.post("/similar")
def similar():
    raise NotImplementedError


@app.post("/risk-score")
def risk_score():
    raise NotImplementedError


@app.post("/package-build")
def package_build():
    raise NotImplementedError
