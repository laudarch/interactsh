package server

import (
	"bytes"
	"crypto/tls"
	"net"
	"strings"
	"time"

	"git.mills.io/prologic/smtpd"
	jsoniter "github.com/json-iterator/go"
	"github.com/projectdiscovery/gologger"
	"github.com/projectdiscovery/interactsh/pkg/server/acme"
	"github.com/projectdiscovery/nebula"
	"github.com/prologic/smtpd"
)

// SMTPServer is a smtp server instance that listens both
// TLS and Non-TLS based servers.
type SMTPServer struct {
	options       *Options
	port25server  smtpd.Server
	port587server smtpd.Server
}

// NewSMTPServer returns a new TLS & Non-TLS SMTP server.
func NewSMTPServer(options *Options) (*SMTPServer, error) {
	server := &SMTPServer{options: options}

	authHandler := func(remoteAddr net.Addr, mechanism string, username []byte, password []byte, shared []byte) (bool, error) {
		return true, nil
	}
	rcptHandler := func(remoteAddr net.Addr, from string, to string) bool {
		return true
	}
	server.port25server = smtpd.Server{
		Addr:        options.ListenIP + ":25",
		AuthHandler: authHandler,
		HandlerRcpt: rcptHandler,
		Hostname:    options.Domain,
		Appname:     "interactsh",
		Handler:     smtpd.Handler(server.defaultHandler),
	}
	server.port587server = smtpd.Server{
		Addr:        options.ListenIP + ":587",
		AuthHandler: authHandler,
		HandlerRcpt: rcptHandler,
		Hostname:    options.Domain,
		Appname:     "interactsh",
		Handler:     smtpd.Handler(server.defaultHandler),
	}
	return server, nil
}

// ListenAndServe listens on smtp and/or smtps ports for the server.
func (h *SMTPServer) ListenAndServe(autoTLS *acme.AutoTLS) {
	go func() {
		if autoTLS == nil {
			return
		}
		srv := &smtpd.Server{Addr: h.options.ListenIP + ":465", Handler: h.defaultHandler, Appname: "interactsh", Hostname: h.options.Domain}
		srv.TLSConfig = &tls.Config{}
		srv.TLSConfig.GetCertificate = autoTLS.GetCertificateFunc()

		err := srv.ListenAndServe()
		if err != nil {
			gologger.Error().Msgf("Could not serve smtp with tls on port 465: %s\n", err)
		}
	}()

	go func() {
		if err := h.port25server.ListenAndServe(); err != nil {
			gologger.Error().Msgf("Could not serve smtp on port 25: %s\n", err)
		}
	}()
	if err := h.port587server.ListenAndServe(); err != nil {
		gologger.Error().Msgf("Could not serve smtp on port 587: %s\n", err)
	}
}

// defaultHandler is a handler for default collaborator requests
func (h *SMTPServer) defaultHandler(remoteAddr net.Addr, from string, to []string, data []byte) error {
	var uniqueID, fullID, correlationID string

	gologger.Debug().Msgf("New SMTP request: %s %s %s %s\n", remoteAddr, from, to, string(data))

	for _, addr := range to {
		if len(addr) > 33 && strings.Contains(addr, "@") {
			parts := strings.Split(addr[strings.Index(addr, "@")+1:], ".")
			for i, part := range parts {
				if len(part) == 33 {
					uniqueID = part
					correlationID = uniqueID[:20]
					fullID = part
					if i+1 <= len(parts) {
						fullID = strings.Join(parts[:i+1], ".")
					}
				}
			}
		}
	}

	item, err := h.options.Storage.GetCorrelationDataByID(correlationID)
	if err == nil {
		// Handle callbacks - SMTP server provided only standard response, so here we match and continue
		for _, callback := range item.Callbacks {
			mapDSL := make(map[string]interface{})
			mapDSL["remote_addr"] = remoteAddr.String()
			mapDSL["from"] = from
			mapDSL["to"] = to
			mapDSL["data"] = string(data)
			match, err := nebula.EvalAsBool(callback.DSL, mapDSL)
			if err != nil {
				gologger.Warning().Msgf("coudln't evaluate dsl matching: %s\n", err)
			}
			if match {
				// TBD - For now just triggering the callback
				_, err := nebula.Eval(callback.Code, mapDSL)
				if err != nil {
					gologger.Warning().Msgf("coudln't execute the callback: %s\n", err)
					return err
				}
			}
		}
	} else {
		gologger.Warning().Msgf("No item found for %s: %s\n", correlationID, err)
	}

	if uniqueID != "" {
		host, _, _ := net.SplitHostPort(remoteAddr.String())
		interaction := &Interaction{
			Protocol:      "smtp",
			UniqueID:      uniqueID,
			FullId:        fullID,
			RawRequest:    string(data),
			SMTPFrom:      from,
			RemoteAddress: host,
			Timestamp:     time.Now(),
		}
		buffer := &bytes.Buffer{}
		if err := jsoniter.NewEncoder(buffer).Encode(interaction); err != nil {
			gologger.Warning().Msgf("Could not encode smtp interaction: %s\n", err)
		} else {
			gologger.Debug().Msgf("%s\n", buffer.String())
			if err := h.options.Storage.AddInteraction(correlationID, buffer.Bytes()); err != nil {
				gologger.Warning().Msgf("Could not store smtp interaction: %s\n", err)
			}
		}
	}
	return nil
}
