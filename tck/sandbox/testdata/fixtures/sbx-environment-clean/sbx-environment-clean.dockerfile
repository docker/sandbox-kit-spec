FROM docker/sandbox-templates:claude-code-docker AS rootfs
USER root
RUN groupadd --gid 1234 sbxagent \
 && useradd --create-home --uid 1234 --gid 1234 --shell /bin/bash sbxagent \
 && touch /etc/sbx-env-persistent.sh

# Copy the root filesystem while dropping inherited image ENV, including
# IS_SANDBOX that an agent template may already declare.
FROM scratch
COPY --from=rootfs / /
COPY --chmod=755 <<'PROBE' /usr/local/bin/kit-tck-sbx-env
#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
import time

names = ("WORKSPACE_DIR", "SANDBOX_NAME", "SANDBOX_ID", "IS_SANDBOX")
env = {name: os.environ[name] for name in names if name in os.environ}
path = Path("/var/tmp/sbx-agent.env")
if sys.argv[1] == "launch":
    # Readers use existence as readiness, so publish only complete JSON.
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(env))
    temporary.replace(path)
    os.execlp("sleep", "sleep", "infinity")
elif sys.argv[1] == "current":
    print(json.dumps(env))
else:
    for _ in range(100):
        if path.exists():
            break
        time.sleep(0.1)
    print(path.read_text())
PROBE
ENV PATH=/usr/local/bin:/usr/bin:/bin HOME=/home/sbxagent BASH_ENV=/etc/sbx-env-persistent.sh
USER sbxagent
WORKDIR /home/sbxagent/project
ENTRYPOINT ["kit-tck-sbx-env", "launch"]
