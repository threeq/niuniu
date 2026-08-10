package service

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImapQuote_Escapes(t *testing.T) {
	assert.Equal(t, `"a@x.com"`, imapQuote("a@x.com"))
	assert.Equal(t, `"p\"\\x"`, imapQuote(`p"\x`))
}

func TestImapLoginExchange_OK(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a1 OK LOGIN completed\r\n"))
	var w bytes.Buffer
	err := imapLoginExchange(r, &w, "a@x.com", "s3cret")
	require.NoError(t, err)
	assert.Contains(t, w.String(), `a1 LOGIN "a@x.com" "s3cret"`, "sends quoted LOGIN")
	assert.Contains(t, w.String(), "a2 LOGOUT", "logs out after")
}

func TestImapLoginExchange_AuthRejected(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a1 NO [AUTHENTICATIONFAILED] Invalid credentials\r\n"))
	var w bytes.Buffer
	err := imapLoginExchange(r, &w, "u", "wrong")
	var authErr *ErrIMAPAuth
	require.ErrorAs(t, err, &authErr, "NO reply maps to ErrIMAPAuth")
	assert.Contains(t, authErr.Error(), "Invalid credentials")
}

func TestImapLoginExchange_SkipsUntaggedThenOK(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("* CAPABILITY IMAP4rev1\r\na1 OK done\r\n"))
	var w bytes.Buffer
	require.NoError(t, imapLoginExchange(r, &w, "u", "p"))
}

func TestImapLoginExchange_BadReply(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a1 BAD syntax error\r\n"))
	var w bytes.Buffer
	var authErr *ErrIMAPAuth
	require.ErrorAs(t, imapLoginExchange(r, &w, "u", "p"), &authErr)
}
