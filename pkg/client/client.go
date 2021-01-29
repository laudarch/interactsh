package client

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
	"github.com/projectdiscovery/interactsh/pkg/server"
	"github.com/projectdiscovery/retryablehttp-go"
	"github.com/rs/xid"
	"gopkg.in/corvus-ch/zbase32.v1"
)

var objectIDCounter = uint32(0)

// Client is a client for communicating with interactsh server instance.
type Client struct {
	correlationID     string
	serverURL         *url.URL
	httpClient        *retryablehttp.Client
	privateKey        *rsa.PrivateKey
	persistentSession bool
}

// Options contains configuration options for interactsh client
type Options struct {
	// ServerURL is the URL for the interactsh server.
	ServerURL string
	// PersistentSession keeps the session open for future requests.
	PersistentSession bool
}

func init() {
	objectIDCounter = randInt()
}

// New creates a new client instance based on provided options
func New(options *Options) (*Client, error) {
	parsed, err := url.Parse(options.ServerURL)
	if err != nil {
		return nil, errors.Wrap(err, "could not parse server URL")
	}
	// Generate a random ksuid which will be used as server secret.
	client := &Client{
		serverURL:         parsed,
		correlationID:     xid.New().String(),
		persistentSession: options.PersistentSession,
		httpClient:        retryablehttp.NewClient(retryablehttp.DefaultOptionsSingle),
	}
	// Generate an RSA Public / Private key for interactsh client
	if err := client.generateRSAKeyPair(); err != nil {
		return nil, err
	}
	return client, nil
}

// Close closes the collaborator client and deregisters from the
// collaborator server if not explicitly asked by the user.
func (c *Client) Close() error {
	if !c.persistentSession {
		register := server.DeregisterRequest{
			CorrelationID: c.correlationID,
		}
		data, err := jsoniter.Marshal(register)
		if err != nil {
			return errors.Wrap(err, "could not marshal deregister request")
		}
		URL := c.serverURL.String() + "deregister"
		req, err := retryablehttp.NewRequest("POST", URL, bytes.NewReader(data))
		if err != nil {
			return errors.Wrap(err, "could not create new request")
		}
		req.ContentLength = int64(len(data))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return errors.Wrap(err, "could not make deregister request")
		}
		if resp.StatusCode != 200 {
			return errors.Wrap(err, "could not deregister to server")
		}
	}
	return nil
}

// generateRSAKeyPair generates an RSA public-private keypair and
// registers the current client with the master server using the
// provided RSA Public Key as well as Correlation Key.
func (c *Client) generateRSAKeyPair() error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return errors.Wrap(err, "could not create rsa private key")
	}

	buffer := &bytes.Buffer{}
	if err := gob.NewEncoder(buffer).Encode(privateKey.Public()); err != nil {
		return errors.Wrap(err, "could not encode rsa public key")
	}

	register := server.RegisterRequest{
		PublicKey:     buffer.Bytes(),
		CorrelationID: c.correlationID,
	}
	data, err := jsoniter.Marshal(register)
	if err != nil {
		return errors.Wrap(err, "could not marshal register request")
	}
	URL := c.serverURL.String() + "register"
	req, err := retryablehttp.NewRequest("POST", URL, bytes.NewReader(data))
	if err != nil {
		return errors.Wrap(err, "could not create new request")
	}
	req.ContentLength = int64(len(data))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "could not make register request")
	}
	if resp.StatusCode != 200 {
		return errors.Wrap(err, "could not register to server")
	}
	return nil
}

// URL returns a new URL that can be be used for external interaction requests.
func (c *Client) URL() string {
	random := make([]byte, 7)
	binary.BigEndian.PutUint32(random[:], uint32(time.Now().Unix()))
	i := atomic.AddUint32(&objectIDCounter, 1)
	random[4] = byte(i >> 16)
	random[5] = byte(i >> 8)
	random[6] = byte(i)

	builder := &strings.Builder{}
	builder.WriteString(c.correlationID)
	builder.WriteString(zbase32.StdEncoding.EncodeToString(random))
	builder.WriteString(".")
	builder.WriteString("")
	builder.WriteString(c.serverURL.Host)
	URL := builder.String()
	return URL
}

// URLReflection returns a reversed part of the URL payload
func URLReflection(URL string) string {
	parts := strings.Split(URL, ".")
	var randomID string
	for _, part := range parts {
		if len(part) == 32 {
			randomID = part
		}
	}
	rns := []rune(randomID)
	for i, j := 0, len(rns)-1; i < j; i, j = i+1, j-1 {
		rns[i], rns[j] = rns[j], rns[i]
	}
	return string(rns)
}

// randInt generates a random uint32
func randInt() uint32 {
	b := make([]byte, 3)
	if _, err := rand.Reader.Read(b); err != nil {
		panic(fmt.Errorf("xid: cannot generate random number: %v;", err))
	}
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}