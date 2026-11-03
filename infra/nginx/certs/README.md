# Self-signed certs for local/dev TLS (TLS 1.3).
# Regenerate with:
#   openssl req -x509 -newkey rsa:2048 -nodes \
#     -keyout infra/nginx/certs/server.key -out infra/nginx/certs/server.crt \
#     -days 365 -subj "/CN=localhost"
# In production replace with a CA-issued certificate.
mkdir -p infra/nginx/certs