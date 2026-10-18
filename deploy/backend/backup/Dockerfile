FROM postgres:18.6@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336
ARG VCS_REF=unknown
LABEL org.opencontainers.image.revision=$VCS_REF
# The PostgreSQL image has no CA bundle; bootstrap TLS from the pinned edge image.
COPY --from=caddy:2.11.7@sha256:685ae3d7c34d9b5f8caeddc5d14051db7bbd88ff1f5a2fa0c16bd8ca328fb7af \
    /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|URIs: http://deb.debian.org/|URIs: https://deb.debian.org/|' /etc/apt/sources.list.d/debian.sources \
    && sed -i 's|http://apt.postgresql.org/|https://apt.postgresql.org/|' /etc/apt/sources.list.d/pgdg.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends age ca-certificates python3 python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/backup-venv \
    && mkdir -p /state \
    && chown postgres:postgres /state
COPY requirements.txt /opt/backup/requirements.txt
RUN /opt/backup-venv/bin/pip install --no-cache-dir --require-hashes -r /opt/backup/requirements.txt
COPY backup.py /opt/backup/backup.py
USER postgres:postgres
ENV PYTHONDONTWRITEBYTECODE=1 BACKUP_STATE_DIR=/state
ENTRYPOINT ["/opt/backup-venv/bin/python", "/opt/backup/backup.py"]
