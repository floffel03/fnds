// Copyright 2016 Florian Minnecker. All rights reserved.
// some portions of this software are adapted from https://github.com/miekg/exdns/blob/master/reflect/reflect.go

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
	"io/ioutil"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	tsig   *string
	driver *cache.Cache
)

type Conf struct {
	Domains []Domain      `json:"domains"`
	Listen  string        `json:"listen"`
	TTL     time.Duration `json:"ttl"`
	Purge   time.Duration `json:"cachepurge"`
}

type Domain struct {
	Name    string   `json:"name"`
	A       A        `json:"a"`
	Mx      MX       `json:"mx"`
	Domains []Domain `json:"domains"`
}

type A struct {
	Ip  net.IP        `json:"ip"`
	Ttl time.Duration `json:"ttl"`
}

type MX struct {
	Content  string        `json:"content"`
	Priority uint16        `json:"priority"`
	Ttl      time.Duration `json:"ttl"`
}

// New... methods adapted from https://github.com/skynetservices/skydns/blob/master/msg/service.go
// NewSRV returns a new SRV record based on the Service.
/*func (s *Service) NewSRV(name string, weight uint16) *dns.SRV {
	host := targetStrip(dns.Fqdn(s.Host), s.TargetStrip)

	return &dns.SRV{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: s.Ttl},
		Priority: uint16(s.Priority), Weight: weight, Port: uint16(s.Port), Target: host}
}
*/

// NewMX returns a new MX record based on the Service.
func NewMX(host string, name string, priority uint16, ttl uint32) *dns.MX {
	//host := targetStrip(dns.Fqdn(s.Host), s.TargetStrip)

	return &dns.MX{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeMX, Class: dns.ClassINET, Ttl: ttl},
		Preference: priority, Mx: host}
}

// NewA returns a new A record based on the Service.
func NewA(name string, ip net.IP, ttl uint32) *dns.A {
	return &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl}, A: ip}
}

// NewAAAA returns a new AAAA record based on the Service.
func NewAAAA(name string, ip net.IP, ttl uint32) *dns.AAAA {
	return &dns.AAAA{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl}, AAAA: ip}
}

// NewCNAME returns a new CNAME record based on the Service.
func NewCNAME(name string, target string, ttl uint32) *dns.CNAME {
	return &dns.CNAME{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: ttl}, Target: target}
}

// NewNS returns a new NS record based on the Service.
func NewNS(name string, target string, ttl uint32) *dns.NS {
	return &dns.NS{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: ttl}, Ns: target}
}

// NewTXT returns a new TXT record based on the Service.
func NewTXT(name string, text string, ttl uint32) *dns.TXT {
	return &dns.TXT{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: ttl}, Txt: split255(text)}
}

// Split255 splits a string into 255 byte chunks.
func split255(s string) []string {
	if len(s) < 255 {
		return []string{s}
	}
	sx := []string{}
	p, i := 0, 255
	for {
		if i <= len(s) {
			sx = append(sx, s[p:i])
		} else {
			sx = append(sx, s[p:])
			break

		}
		p, i = p+255, i+255
	}

	return sx
}

func serve(net, name, secret string) {
	server := &dns.Server{Addr: ":53", Net: net, TsigSecret: map[string]string{name: secret}}
	err := server.ListenAndServe()
	if err != nil {
		fmt.Printf("Unable to start %s Server: %s\n", net, err.Error())
	}
}

func (dom Domain) addHandler(tld string) {
	fqdn := dom.Name
	if tld != "" {
		fqdn = dom.Name + "." + tld
	}

	FqdnParts, _ := dns.IsDomainName(fqdn)

	fmt.Printf("Adding: %v - %v nums\n", fqdn, FqdnParts)

	// Handle dns requests if it is really a fqdn
	if dns.IsFqdn(fqdn) {
		dns.HandleFunc(fqdn, func(w dns.ResponseWriter, req *dns.Msg) {
			m := new(dns.Msg)
			m.SetReply(req)
			m.Compress = true

			for i, q := range req.Question {
				fmt.Printf("Requested: %s, Type: %v\n", req.Question[i].Name, req.Question[i].Qtype)

				switch q.Qtype {
				case 1:
					fmt.Printf("Adding a record %v with ip %v", q.Name, dom.A.Ip)
					m.Answer = append(m.Answer, NewA(q.Name, dom.A.Ip, uint32(dom.A.Ttl)))
				case 15:
					fmt.Printf("Adding a record %v with ip %v", q.Name, dom.A.Ip)
					m.Answer = append(m.Answer, NewMX(q.Name, dom.Mx.Content, dom.Mx.Priority, uint32(dom.Mx.Ttl)))
				}
			}

			w.WriteMsg(m)
		})
	}

	// add subdomains..
	for _, d := range dom.Domains {
		d.addHandler(fqdn)
	}
}

func main() {
	tsig := flag.String("tsig", "", "use MD5 hmac tsig: keyname:base64")
	var name, secret string

	flag.Usage = func() {
		flag.PrintDefaults()
	}
	flag.Parse()

	if *tsig != "" {
		a := strings.SplitN(*tsig, ":", 2)
		name, secret = dns.Fqdn(a[0]), a[1]
	}

	/// Load the configuration into the "conf"-var
	confBytes, _ := ioutil.ReadFile("dnsconfig.json")
	conf := Conf{}
	json.Unmarshal(confBytes, &conf)

	fmt.Printf("json unmasheld: \n%v\n\n\n", conf)

	// initialize the cache with the configurated options..
	cache.New(conf.TTL*time.Second, conf.Purge*time.Second)

	// Handle domain names
	for _, dom := range conf.Domains {
		dom.addHandler("")
	}

	// Start listening...
	go serve("tcp", name, secret)
	go serve("udp", name, secret)

	sig := make(chan os.Signal)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

forever:
	for {
		select {
		case s := <-sig:
			fmt.Printf("Signal (%s) received, stopping\n", s)
			break forever
		}
	}

}
