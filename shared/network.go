package shared

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

func RFC3493Dialer(network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		c, err := net.DialTimeout(network, net.JoinHostPort(a, port), 10*time.Second)
		if err != nil {
			continue
		}
		return c, err
	}
	return nil, fmt.Errorf("Unable to connect to: " + address)
}

func initTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA},
		PreferServerCipherSuites: true,
	}
}

func finalizeTLSConfig(tlsConfig *tls.Config, tlsRemoteCert *x509.Certificate) {
	// Trusted certificates
	if tlsRemoteCert != nil {
		caCertPool := tlsConfig.RootCAs
		if caCertPool == nil {
			caCertPool = x509.NewCertPool()
		}

		// Make it a valid RootCA
		tlsRemoteCert.IsCA = true
		tlsRemoteCert.KeyUsage = x509.KeyUsageCertSign

		// Setup the pool
		caCertPool.AddCert(tlsRemoteCert)
		tlsConfig.RootCAs = caCertPool

		// Set the ServerName
		if tlsRemoteCert.DNSNames != nil {
			tlsConfig.ServerName = tlsRemoteCert.DNSNames[0]
		}
	}

	tlsConfig.BuildNameToCertificate()
}

func GetTLSConfig(tlsClientCertFile string, tlsClientKeyFile string, tlsClientCAFile string, tlsRemoteCert *x509.Certificate) (*tls.Config, error) {
	tlsConfig := initTLSConfig()

	// Client authentication
	if tlsClientCertFile != "" && tlsClientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(tlsClientCertFile, tlsClientKeyFile)
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if tlsClientCAFile != "" {
		caCertificates, err := ioutil.ReadFile(tlsClientCAFile)
		if err != nil {
			return nil, err
		}

		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(caCertificates)

		tlsConfig.RootCAs = caPool
	}

	finalizeTLSConfig(tlsConfig, tlsRemoteCert)
	return tlsConfig, nil
}

func GetTLSConfigMem(tlsClientCert string, tlsClientKey string, tlsClientCA string, tlsRemoteCertPEM string) (*tls.Config, error) {
	tlsConfig := initTLSConfig()

	// Client authentication
	if tlsClientCert != "" && tlsClientKey != "" {
		cert, err := tls.X509KeyPair([]byte(tlsClientCert), []byte(tlsClientKey))
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	var tlsRemoteCert *x509.Certificate
	if tlsRemoteCertPEM != "" {
		// Ignore any content outside of the PEM bytes we care about
		certBlock, _ := pem.Decode([]byte(tlsRemoteCertPEM))
		if certBlock == nil {
			return nil, fmt.Errorf("Invalid remote certificate")
		}

		var err error
		tlsRemoteCert, err = x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, err
		}
	}

	if tlsClientCA != "" {
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM([]byte(tlsClientCA))

		tlsConfig.RootCAs = caPool
	}

	finalizeTLSConfig(tlsConfig, tlsRemoteCert)

	return tlsConfig, nil
}

func IsLoopback(iface *net.Interface) bool {
	return int(iface.Flags&net.FlagLoopback) > 0
}

func WebsocketSendStream(conn *websocket.Conn, r io.Reader, bufferSize int) chan bool {
	ch := make(chan bool)

	if r == nil {
		close(ch)
		return ch
	}

	go func(conn *websocket.Conn, r io.Reader) {
		in := ReaderToChannel(r, bufferSize)
		for {
			buf, ok := <-in
			if !ok {
				break
			}

			w, err := conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				LogDebugf("Got error getting next writer %s", err)
				break
			}

			_, err = w.Write(buf)
			w.Close()
			if err != nil {
				LogDebugf("Got err writing %s", err)
				break
			}
		}
		conn.WriteMessage(websocket.TextMessage, []byte{})
		ch <- true
	}(conn, r)

	return ch
}

func WebsocketRecvStream(w io.Writer, conn *websocket.Conn) chan bool {
	ch := make(chan bool)

	go func(w io.Writer, conn *websocket.Conn) {
		for {
			mt, r, err := conn.NextReader()
			if mt == websocket.CloseMessage {
				LogDebugf("Got close message for reader")
				break
			}

			if mt == websocket.TextMessage {
				LogDebugf("got message barrier")
				break
			}

			if err != nil {
				LogDebugf("Got error getting next reader %s, %s", err, w)
				break
			}

			buf, err := ioutil.ReadAll(r)
			if err != nil {
				LogDebugf("Got error writing to writer %s", err)
				break
			}

			if w == nil {
				continue
			}

			i, err := w.Write(buf)
			if i != len(buf) {
				LogDebugf("Didn't write all of buf")
				break
			}
			if err != nil {
				LogDebugf("Error writing buf %s", err)
				break
			}
		}
		ch <- true
	}(w, conn)

	return ch
}

func defaultReader(conn *websocket.Conn, r io.ReadCloser, readDone chan<- bool) {
	/* For now, we don't need to adjust buffer sizes in
	* WebsocketMirror, since it's used for interactive things like
	* exec.
	 */
	in := ReaderToChannel(r, -1)
	for {
		buf, ok := <-in
		if !ok {
			r.Close()
			LogDebugf("sending write barrier")
			conn.WriteMessage(websocket.TextMessage, []byte{})
			readDone <- true
			return
		}
		w, err := conn.NextWriter(websocket.BinaryMessage)
		if err != nil {
			LogDebugf("Got error getting next writer %s", err)
			break
		}

		_, err = w.Write(buf)
		w.Close()
		if err != nil {
			LogDebugf("Got err writing %s", err)
			break
		}
	}
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	conn.WriteMessage(websocket.CloseMessage, closeMsg)
	readDone <- true
	r.Close()
}

func defaultWriter(conn *websocket.Conn, w io.WriteCloser, writeDone chan<- bool) {
	for {
		mt, r, err := conn.NextReader()
		if err != nil {
			LogDebugf("Got error getting next reader %s, %s", err, w)
			break
		}

		if mt == websocket.CloseMessage {
			LogDebugf("Got close message for reader")
			break
		}

		if mt == websocket.TextMessage {
			LogDebugf("Got message barrier, resetting stream")
			break
		}

		buf, err := ioutil.ReadAll(r)
		if err != nil {
			LogDebugf("Got error writing to writer %s", err)
			break
		}
		i, err := w.Write(buf)
		if i != len(buf) {
			LogDebugf("Didn't write all of buf")
			break
		}
		if err != nil {
			LogDebugf("Error writing buf %s", err)
			break
		}
	}
	writeDone <- true
	w.Close()
}

// WebsocketMirror allows mirroring a reader to a websocket and taking the
// result and writing it to a writer. This function allows for multiple
// mirrorings and correctly negotiates stream endings. However, it means any
// websocket.Conns passed to it are live when it returns, and must be closed
// explicitly.
type WebSocketMirrorReader func(conn *websocket.Conn, r io.ReadCloser, readDone chan<- bool)
type WebSocketMirrorWriter func(conn *websocket.Conn, w io.WriteCloser, writeDone chan<- bool)

func WebsocketMirror(conn *websocket.Conn, w io.WriteCloser, r io.ReadCloser, Reader WebSocketMirrorReader, Writer WebSocketMirrorWriter) (chan bool, chan bool) {
	readDone := make(chan bool, 1)
	writeDone := make(chan bool, 1)

	ReadFunc := Reader
	if ReadFunc == nil {
		ReadFunc = defaultReader
	}

	WriteFunc := Writer
	if WriteFunc == nil {
		WriteFunc = defaultWriter
	}

	go ReadFunc(conn, r, readDone)
	go WriteFunc(conn, w, writeDone)

	return readDone, writeDone
}
