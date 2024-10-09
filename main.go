package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ThalesIgnite/crypto11"
)

func timedLog(message string) {
	fmt.Printf("%v - %s\n", time.Now(), message)
}

func listCertificates(pkcs11path, tokenSerial *string, pinVal string) {
	config := crypto11.Config{
		Path:        *pkcs11path,
		TokenSerial: *tokenSerial,
		Pin:         pinVal,
	}

	context, err := crypto11.Configure(&config)
	if err != nil {
		log.Fatalln(err)
	}

	certificates, err := context.FindAllPairedCertificates()
	if err != nil {
		log.Fatalln(err)
	}

	index := 0
	for _, cert := range certificates {
		fmt.Printf("Certificate index %d: %v\n", index, cert.Leaf.Subject)
		index++
	}
}

func modifyResponse(destinationUrl *url.URL) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.Header.Get("Location") != "" {
			newLocation := strings.Replace(resp.Header.Get("Location"), destinationUrl.String(), "", 1)
			resp.Header.Set("Location", newLocation)
		}
		// Disable secure cookies
		cookies := resp.Cookies()
		if len(cookies) > 0 {
			resp.Header.Del("Set-Cookie")
		}
		for _, cookie := range cookies {
			if cookie.Secure {
				cookie.Secure = false
			}
			resp.Header.Add("Set-Cookie", cookie.String())
		}
		return nil
	}
}

func main() {
	listenAddress := flag.String("listen-addr", "127.0.0.1", "Address to listen on")
	listenPort := flag.Int("listen-port", 8080, "Port to listen on")
	pkcs11path := flag.String("pkcs11-path", "", "Path to the PKCS11 module. Use the card vendor-specific one, or run 'pkcs11-tool --help' and look for '--module' default value for a good one to use.")
	tokenSerial := flag.String("token-serial", "", "Serial number of the token. Run 'pkcs11-tool --list-token-slots' to find it.")
	certificateIndex := flag.Int("certificate-index", 0, fmt.Sprintf("Index of the certificate to use. Run '%s -token-serial ... [-pin/-pin-file] ... list-certificates' to find the index. By default, the first found certificate (index 0) will be used.", os.Args[0]))
	pin := flag.String("pin", "", "PIN to access the card. Cannot be used with --pin-file.")
	pinFile := flag.String("pin-file", "", "File containing the PIN to access the card (will be deleted after read!). Cannot be used with --pin.")
	destinationUrl := flag.String("destination-url", "", "URL to forward requests to.")
	noPreserveHost := flag.Bool("no-preserve-host", false, "Do not preserve the host header in the request.")
	logRequests := flag.Bool("log-requests", false, "Log each request to stdout.")
	listenTLS := flag.Bool("listen-tls", false, "Listen on TLS instead of plain HTTP (useful if your upstream sets 'secure' cookies")
	listenTLSCertificate := flag.String("listen-tls-cert", "", "Path to the certificate or chain file for the TLS listener (required if --listen-tls is set)")
	listenTLSPrivateKey := flag.String("listen-tls-key", "", "Path to the private key file for the TLS listener (required if --listen-tls is set)")
	flag.Parse()

	if *pkcs11path == "" {
		fmt.Println("pkcs11-path is required")
		flag.Usage()
		return
	}

	if *tokenSerial == "" {
		fmt.Println("token-serial is required")
		flag.Usage()
		return
	}

	if *pin == "" && *pinFile == "" {
		fmt.Println("Either pin or pin-file is required")
		flag.Usage()
		return
	}

	if *pin != "" && *pinFile != "" {
		fmt.Println("Both pin and pin-file are set. Please use only one")
		flag.Usage()
		return
	}

	pinVal := *pin

	if *pinFile != "" {
		pinBytes, err := os.ReadFile(*pinFile)
		if err != nil {
			log.Fatalf("Error reading pin file: %v", err)
		}
		pinVal = strings.TrimSpace(string(pinBytes))
		err = os.Remove(*pinFile)
		if err != nil {
			log.Fatalf("Error deleting pin file: %v", err)
		}
	}

	if flag.Arg(0) == "list-certificates" {
		listCertificates(pkcs11path, tokenSerial, pinVal)
		return
	}

	if *destinationUrl == "" {
		fmt.Println("destination-url is required")
		flag.Usage()
		return
	}

	if *listenTLS {
		if *listenTLSPrivateKey == "" || *listenTLSCertificate == "" {
			fmt.Println("listen-tls-private-key and listen-tls-certificate are required when listen-tls is set")
			flag.Usage()
			return
		}
	}

	timedLog("Reverse proxy is starting")
	config := crypto11.Config{
		Path:        *pkcs11path,
		TokenSerial: *tokenSerial,
		Pin:         pinVal,
	}

	context, err := crypto11.Configure(&config)
	if err != nil {
		log.Fatalln(err)
	}

	certificates, err := context.FindAllPairedCertificates()
	if err != nil {
		log.Fatalln(err)
	}

	if *certificateIndex >= len(certificates) {
		log.Fatalf("Certificate index %d is out of range. Run '%s -token-serial ... [-pin/-pin-file] ... list-certificates' to find the index.\n", *certificateIndex, os.Args[0])
		return
	}
	cert := certificates[*certificateIndex]
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates:  []tls.Certificate{cert},
			Renegotiation: tls.RenegotiateOnceAsClient,
		},
	}

	destUrl, err := url.Parse(*destinationUrl)
	if err != nil {
		log.Fatalln(err)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(destUrl)
	proxy.Transport = transport

	handler := func(p *httputil.ReverseProxy) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if !*noPreserveHost {
				r.Host = destUrl.Host
			}
			if *logRequests {
				timedLog(fmt.Sprintf("Request: %s %s", r.Method, r.URL.String()))
			}
			p.ServeHTTP(w, r)
		}
	}
	proxy.ModifyResponse = modifyResponse(destUrl)

	http.HandleFunc("/", handler(proxy))
	if *listenTLS {
		timedLog(fmt.Sprintf("Listening on %s:%d over TLS", *listenAddress, *listenPort))
		log.Fatal(http.ListenAndServeTLS(fmt.Sprintf("%s:%d", *listenAddress, *listenPort), *listenTLSCertificate, *listenTLSPrivateKey, nil))
	} else {
		timedLog(fmt.Sprintf("Listening on %s:%d", *listenAddress, *listenPort))
		log.Fatal(http.ListenAndServe(fmt.Sprintf("%s:%d", *listenAddress, *listenPort), nil))
	}
}
