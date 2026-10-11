FROM alpine:3.20
# sshd keeps the first value it reads for each option, so remove the stock
# settings we override before appending ours.
RUN apk add --no-cache openssh && ssh-keygen -A \
 && adduser -D -s /bin/sh squi && echo 'squi:squi' | chpasswd \
 && sed -i -E '/^#?[[:space:]]*(PasswordAuthentication|KbdInteractiveAuthentication|AllowTcpForwarding|PubkeyAuthentication|AuthorizedKeysFile)[[:space:]]/d' /etc/ssh/sshd_config \
 && printf 'PasswordAuthentication yes\nKbdInteractiveAuthentication yes\nAllowTcpForwarding yes\nPubkeyAuthentication yes\nAuthorizedKeysFile /etc/ssh/authorized_keys/%%u\n' >> /etc/ssh/sshd_config
EXPOSE 22
CMD ["/bin/sh", "-c", "mkdir -p /etc/ssh/authorized_keys && cp /keys/id_ed25519.pub /etc/ssh/authorized_keys/squi && chmod 644 /etc/ssh/authorized_keys/squi && exec /usr/sbin/sshd -D -e"]
