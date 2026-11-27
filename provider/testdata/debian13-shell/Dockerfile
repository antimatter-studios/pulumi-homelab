# A disposable Debian 13 machine for the resources' integration tests: a real shell, coreutils,
# sudo for escalation, and acl for setfacl/getfacl. The tests run commands in it with docker exec.
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends sudo acl \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash chris \
 && echo 'chris ALL=(root) NOPASSWD: ALL' > /etc/sudoers.d/chris
CMD ["sleep", "infinity"]
