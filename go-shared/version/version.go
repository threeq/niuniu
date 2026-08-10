package version

// Protocol is the tunnel control-plane wire protocol version negotiated in HELLO.
const Protocol uint16 = 1

// MinSupported is the oldest Protocol version this binary still accepts.
const MinSupported uint16 = 1
