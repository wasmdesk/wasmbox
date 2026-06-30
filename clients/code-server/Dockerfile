# Dockerfile mirror for the wasmbox VS Code launcher's upstream.
#
# The wasmbox 'vscode' dom-window LAUNCHABLE entry points at a reverse-
# proxy of an upstream code-server. This image bakes the CLI flags the
# wasmbox launcher needs into a code-server build, so a `docker run`
# stands up a server that just works behind wasmdesk-up's proxy.
#
# Build + push to ghcr:
#   docker build -t ghcr.io/wasmdesk/code-server:latest .
#   echo $GHCR_TOKEN | docker login ghcr.io -u tannevaled --password-stdin
#   docker push ghcr.io/wasmdesk/code-server:latest
#
# Run locally:
#   docker run --rm -p 8443:8443 \
#     -v "$PWD:/workspace" \
#     ghcr.io/wasmdesk/code-server:latest
#
# Then start wasmdesk-up with the proxy pointing here:
#   WASMBOX_CODE_SERVER_URL=http://127.0.0.1:8443 wasmdesk-up
#
# In the wasmbox tab the 'VS Code' dock button (or
# `wasmboxSpawnDOMWindow("/code-server/", 1100, 700, "VS Code")` in
# devtools) opens the editor inside a wasmbox window.

FROM codercom/code-server:latest

# Same defaults as the brew-install operator instructions in
# compositor.rb's LAUNCHABLE comment:
#   - --auth none           : no password gate (the wasmlogin portal is
#                             the access control surface)
#   - --bind-addr 0.0.0.0:8443 : reachable from the docker host
#   - --trusted-origins     : accept the wasmbox / wasmaqua origins
#                             so code-server's CSRF check doesn't refuse
#                             same-origin-via-proxy requests
#   - --disable-telemetry   : opt out of upstream telemetry
ENV PORT=8443

ENTRYPOINT ["/usr/bin/entrypoint.sh", \
            "--auth", "none", \
            "--bind-addr", "0.0.0.0:8443", \
            "--trusted-origins", "127.0.0.1:8080,localhost:8080,127.0.0.1:8081,localhost:8081", \
            "--disable-telemetry", \
            "/workspace"]
