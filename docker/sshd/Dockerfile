# Integration-test SSH server (master plan §9, phases 3e/5c).
#
# A minimal openssh-server with a known user/password and a pre-seeded
# $HOME tree for the SFTP integration suite. The container also ships a
# "fake editor" script (dsm-fake-editor) that appends a line to its last
# argument, letting tests exercise the EditRemoteText save-detection path
# without a GUI editor. Fixture sizes: big.txt (3 MiB, over the 2 MiB edit
# cap -> ErrTooLarge), data.bin (starts with NUL bytes -> ErrBinary content
# probe), a.txt / subdir/b.md (editable text), subdir (non-empty for
# ErrDirNotEmpty).
#
# NOTE: this image is for TEST ONLY and intentionally embeds a well-known
# credential (user "test" / password "test").

FROM alpine:3.20

RUN apk add --no-cache openssh bash shadow

# Create the SFTP test user with a known password.
RUN adduser -D -s /bin/sh test \
    && echo 'test:test' | chpasswd \
    && mkdir -p /run/sshd

# Pre-seed the remote $HOME tree and fixtures.
RUN mkdir -p /home/test/subdir/deep \
    && printf 'hello root text\n' > /home/test/a.txt \
    && printf 'hello nested markdown\n' > /home/test/subdir/b.md \
    && printf 'nested deep file\n' > /home/test/subdir/deep/c.conf \
    && dd if=/dev/urandom of=/home/test/big.txt bs=1M count=3 status=none \
    && printf '\x00\x01\x02\x03\xff' > /home/test/data.bin \
    && chown -R test:test /home/test

# NOTE: the fake text editor for EditRemoteText is NOT baked into this image —
# the app launches the configured editor on the HOST. The integration test
# writes its own host-side fake-editor script (see integration_test.go).

COPY sshd_config /etc/ssh/sshd_config

# Generate host keys and expose sshd.
RUN ssh-keygen -A
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]