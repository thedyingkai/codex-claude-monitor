# Firmware trust anchors

`isrgrootx1.pem` is the public ISRG Root X1 certificate used for ordinary
HTTPS endpoints.

For a dedicated device CA, place its **public root certificate only** at
`device-root-ca.pem` before compiling. That filename is ignored by Git because
each deployment has a different trust anchor. Never place the CA private key or
server private key in this repository.

`device-root-ca.example.pem` is a non-production CI fixture. It is not trusted
by any deployed Quota Monitor server and must not be used as a production
trust anchor.
