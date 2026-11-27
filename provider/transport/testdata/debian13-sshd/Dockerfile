# A disposable Debian 13 sshd for the transport's integration tests.
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server sudo \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash chris \
 && echo 'chris ALL=(root) NOPASSWD: ALL' > /etc/sudoers.d/chris \
 && mkdir -p /run/sshd /home/chris/.ssh \
 && chown chris:chris /home/chris/.ssh && chmod 700 /home/chris/.ssh
# The test passes the authorised key in AUTHORIZED_KEY.
CMD ["/bin/sh", "-c", "printf '%s\\n' \"$AUTHORIZED_KEY\" > /home/chris/.ssh/authorized_keys && chown chris:chris /home/chris/.ssh/authorized_keys && chmod 600 /home/chris/.ssh/authorized_keys && exec /usr/sbin/sshd -D -e"]
