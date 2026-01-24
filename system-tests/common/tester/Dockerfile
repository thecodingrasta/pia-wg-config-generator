FROM alpine:3.20
RUN apk add --no-cache bash curl coreutils grep iproute2 ca-certificates wget
WORKDIR /app
COPY verify.sh /app/verify.sh
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/verify.sh /app/entrypoint.sh
ENTRYPOINT ["/app/entrypoint.sh"]