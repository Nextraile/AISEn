FROM python:3.11-slim

# UID container user. Match with host UID if ./data/vector_db is bind-mounted
# if different, SQLite refuses to write and startup fails with "attempt to write a readonly database".
#
# The default stays 10001 so a plain `docker compose up` does not depend on the
# host UID; docker-entrypoint.sh detects the mismatch and prints the fix.

ARG APP_UID=10001

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/model-cache

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu \
    && pip install --no-cache-dir -r requirements.txt

COPY app.py settings.py docker-entrypoint.sh ./
RUN chmod +x docker-entrypoint.sh

# `ingest.py` is not copied: the image only serves. To (re)build the index, run
# `python ingest.py --all` on the host — compose bind-mounts ./data/vector_db.

RUN mkdir -p /app/data/vector_db /model-cache \
    && useradd --create-home --uid ${APP_UID} app \
    && chown -R app:app /app /model-cache

USER app

EXPOSE 8000

# Guard first: explains a UID/permission mismatch in plain words instead of
# letting ChromaDB fail with "attempt to write a readonly database".
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
