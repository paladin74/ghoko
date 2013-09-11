// Copyright 2013 Xing Xing <mikespook@gmail.com>.
// All rights reserved.
// Use of this source code is governed by a commercial
// license that can be found in the LICENSE file.

package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mikespook/golib/iptpool"
	"github.com/mikespook/golib/log"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

var (
	ErrAccessDeny = errors.New("Access Deny")
	ErrPostOnly   = errors.New("POST method only")
)

type httpServer struct {
	conn       net.Listener
	srv        *http.Server
	iptPool    *iptpool.IptPool
	secret     string
	scriptPath string
	hosting    string
}

func NewHook(addr, scriptPath, secret, hosting string) (srv *httpServer) {
	srv = &httpServer{
		srv:        &http.Server{Addr: addr},
		iptPool:    iptpool.NewIptPool(NewLuaIpt),
		scriptPath: scriptPath,
		secret:     secret,
		hosting:    hosting,
	}
	return
}

func (s *httpServer) SetTLS(certFile, keyFile string) (err error) {
	s.srv.TLSConfig = &tls.Config{}
	s.srv.TLSConfig.NextProtos = []string{"http/1.1"}
	s.srv.TLSConfig.Certificates = make([]tls.Certificate, 1)
	s.srv.TLSConfig.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile)
	return
}

func (s *httpServer) Serve() (err error) {
	s.conn, err = net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return
	}
	if s.srv.TLSConfig != nil {
		s.conn = tls.NewListener(s.conn, s.srv.TLSConfig)
	}
	s.iptPool.OnCreate = func(ipt iptpool.ScriptIpt) error {
		ipt.Init(s.scriptPath)
		return nil
	}
	http.HandleFunc("/", s.handler)
	return s.srv.Serve(s.conn)
}

func (s *httpServer) Close() error {
	errstr := ""
	emap := s.iptPool.Free()
	if n := len(emap); n > 0 {
		for k, err := range emap {
			errstr = fmt.Sprintf("%s[%s]: %s\n", errstr, k, err)
		}
	}
	s.conn.Close()
	if errstr != "" {
		return errors.New(errstr)
	}
	return nil
}

func (s *httpServer) precheck(w http.ResponseWriter, r *http.Request) (p string, params url.Values, ok bool) {
	if r.Method != "POST" { // only post method permited
		log.Errorf("[%s] %s \"%s\"", r.RemoteAddr, r.RequestURI,
			ErrPostOnly)
		http.Error(w, ErrPostOnly.Error(), 500)
		ok = false
		return
	}

	u, err := url.Parse(r.RequestURI)
	if err != nil {
		log.Errorf("[%s] %s \"%s\"", r.RemoteAddr, r.RequestURI, err)
		http.Error(w, err.Error(), 500)
		ok = false
		return
	}
	params = u.Query()
	if s.secret != params.Get("secret") { // verify secret token
		log.Errorf("[%s] %s \"%s\"", r.RemoteAddr, r.RequestURI, ErrAccessDeny)
		http.Error(w, ErrAccessDeny.Error(), 403)
		ok = false
		return
	}
	p = u.Path
	params.Del("secret")
	ok = true
	return
}

func (s *httpServer) handler(w http.ResponseWriter, r *http.Request) {
	p, params, ok := s.precheck(w, r)
	if !ok {
		return
	}
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Errorf("[%s] %s \"%s\"", r.RemoteAddr, r.RequestURI, err)
		http.Error(w, err.Error(), 500)
		return
	}
	defer r.Body.Close()

	go func() {
		ipt := s.iptPool.Get()
		defer s.iptPool.Put(ipt)
		repo, name := s.split(p)
		if repo == GITLAB {
			var req GitLabRequest
			err := json.Unmarshal(body, &req)
			if err != nil {
				log.Errorf("[%s] %s \"%s\"", r.RemoteAddr,
					r.RequestURI, err.Error())
				return
			}
			ipt.Bind("Request", &req)
		} else {
			var req GitHubRequest
			err := json.Unmarshal(body, &req)
			if err != nil {
				log.Errorf("[%s] %s \"%s\"", r.RemoteAddr,
					r.RequestURI, err.Error())
				return
			}
			ipt.Bind("Request", &req)
		}
		ipt.Bind("Hosting", repo)
		if err := ipt.Exec(name, params); err != nil {
			log.Errorf("[%s] %s \"%s\"", r.RemoteAddr,
				r.RequestURI, err.Error())
			return
		}
		log.Messagef("[%s] %s \"Success\"", r.RemoteAddr,
			r.RequestURI)
	}()
	w.WriteHeader(200)
}

func (s *httpServer) split(p string) (repo string, name string) {
	name = path.Base(p)
	repo = GITLAB
	sp := strings.SplitN(p, "/", 3)
	switch sp[1] {
	case GITHUB:
		repo = GITHUB
	case GITLAB:
		repo = GITLAB
	default:
		repo = s.hosting
	}
	return
}
