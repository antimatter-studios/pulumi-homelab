# A disposable Debian 13 running a real sshd, for SshdConfig's integration test. There is no systemd
# in a container, so systemctl is a script that manages sshd the way ssh.service does: reload runs
# sshd -t and sends SIGHUP, which makes sshd re-execute itself with the new configuration, and a
# configuration it cannot listen with makes it exit, exactly as on a machine.
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server sudo iproute2 \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash chris \
 && echo 'chris ALL=(root) NOPASSWD: ALL' > /etc/sudoers.d/chris \
 && mkdir -p /run/sshd
COPY systemctl /usr/local/bin/systemctl
CMD ["/bin/sh", "-c", "/usr/sbin/sshd && exec sleep infinity"]
