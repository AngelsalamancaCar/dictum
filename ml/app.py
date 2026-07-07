"""FastAPI entrypoint for the dictum ML worker."""
import tempfile
from pathlib import Path
from typing import Literal

from fastapi import FastAPI, UploadFile
from pydantic import BaseModel

from parsing.liteparse_service import parse_document
from rag.chunking import chunk_document
from rag.embedding_service import embed_passages, embed_queries
from rag.retrieval import Filters, hybrid_search
from classify.knn import classify_by_knn

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


class ChunkRequest(BaseModel):
    text: str


class ChunkResponse(BaseModel):
    chunks: list[dict]


@app.post("/chunk", response_model=ChunkResponse)
def chunk(req: ChunkRequest):
    chunks = chunk_document(req.text)
    return ChunkResponse(chunks=[{"text": c.text, "section_label": c.section_label} for c in chunks])


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


class ClassifyKNNRequest(BaseModel):
    case_summary: str
    k: int = 10


class ClassifyKNNEvidence(BaseModel):
    ruling_id: str
    external_id: str
    similarity: float


class ClassifyKNNResponse(BaseModel):
    case_type: str | None
    confidence: float
    evidence: list[ClassifyKNNEvidence]


@app.post("/classify-knn", response_model=ClassifyKNNResponse)
def classify_knn(req: ClassifyKNNRequest):
    query_vec = embed_queries([req.case_summary])[0]
    result = classify_by_knn(query_vec, k=req.k)
    return ClassifyKNNResponse(
        case_type=result["case_type"],
        confidence=result["confidence"],
        evidence=[
            ClassifyKNNEvidence(
                ruling_id=str(n["id"]), external_id=n["external_id"], similarity=n["similarity"]
            )
            for n in result["evidence"]
        ],
    )


class SimilarRequest(BaseModel):
    case_summary: str
    k: int = 10
    case_type: str | None = None
    court: str | None = None
    date_from: str | None = None
    date_to: str | None = None


class SimilarResult(BaseModel):
    ruling_id: str
    external_id: str
    case_type: str | None
    outcome: str
    court: str | None
    date: str | None
    fused_score: float


class SimilarResponse(BaseModel):
    results: list[SimilarResult]


@app.post("/similar", response_model=SimilarResponse)
def similar(req: SimilarRequest):
    query_vec = embed_queries([req.case_summary])[0]
    filters = Filters(
        case_type=req.case_type, court=req.court, date_from=req.date_from, date_to=req.date_to
    )
    rows = hybrid_search(req.case_summary, query_vec, k=req.k, filters=filters)
    return SimilarResponse(
        results=[
            SimilarResult(
                ruling_id=str(r["id"]),
                external_id=r["external_id"],
                case_type=r["case_type"],
                outcome=r["outcome"],
                court=r["court"],
                date=str(r["date"]) if r["date"] else None,
                fused_score=r["fused_score"],
            )
            for r in rows
        ]
    )


@app.post("/risk-score")
def risk_score():
    raise NotImplementedError


@app.post("/package-build")
def package_build():
    raise NotImplementedError
