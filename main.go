package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nukooo/icy"
)

type status struct{ dj string }

func getStatus() (status, error) {
	r, err := http.Get("https://r-a-d.io/api")
	if err != nil {
		return status{}, err
	}
	defer r.Body.Close()
	var v struct {
		Main struct{ DJ struct{ DJName string } }
	}
	err = json.NewDecoder(r.Body).Decode(&v)
	if err != nil {
		return status{}, err
	}
	return status{v.Main.DJ.DJName}, nil
}

func getStream() (stream io.ReadCloser, metaint int, err error) {
	req, err := http.NewRequest(http.MethodGet, "https://stream.r-a-d.io/main.mp3", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Add("Icy-MetaData", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	metaint, err = strconv.Atoi(resp.Header.Get("icy-metaint"))
	if err != nil {
		resp.Body.Close()
		return nil, 0, err
	}
	return resp.Body, metaint, nil
}

type stateFunc func(status) (stateFunc, error)

func isLive(dj string) bool {
	return dj != "" && dj != "Hanyuu-sama"
}

func wait(s status) (stateFunc, error) {
	if isLive(s.dj) {
		return record(s.dj)
	}
	return wait, nil
}

type cancelReader struct {
	r      io.Reader
	cancel <-chan struct{}
}

func (cr *cancelReader) Read(p []byte) (int, error) {
	select {
	case <-cr.cancel:
		return 0, io.EOF
	default:
		return cr.r.Read(p)
	}
}

var hook func(args ...string)

func record(dj string) (stateFunc, error) {
	go hook("record", dj)

	stream, metaint, err := getStream()
	if err != nil {
		return wait, err
	}

	basename := time.Now().Format(time.RFC3339) + "-" + dj
	audio, err := os.Create(basename + ".mp3")
	if err != nil {
		stream.Close()
		return nil, err
	}
	cue, err := os.Create(basename + ".cue")
	if err != nil {
		audio.Close()
		stream.Close()
		return nil, err
	}

	cancel := make(chan struct{})
	dec := icy.NewDecoder(&cancelReader{stream, cancel}, metaint)
	dec.Audio(audio)
	dec.Metadata(cue)
	dec.Format(icy.CueFormatter(basename+".mp3", true))
	go dec.Decode()

	var fn stateFunc
	fn = func(s status) (stateFunc, error) {
		if s.dj != dj {
			close(cancel)
			cue.Close()
			audio.Close()
			stream.Close()
			if isLive(s.dj) {
				return record(s.dj)
			}
			go hook("done")
			return wait, nil
		}
		return fn, nil
	}

	return fn, nil
}

func main() {
	var hookpath string
	flag.StringVar(&hookpath, "hook", "", "path to hook script")
	flag.Parse()

	if hookpath != "" {
		hook = func(args ...string) {
			log.Println(strings.Join(args, " "))
			err := exec.Command(hookpath, args...).Run()
			if err != nil {
				log.Println(err)
			}
		}
	} else {
		hook = func(args ...string) { log.Println(strings.Join(args, " ")) }
	}

	ch := make(chan status)
	go func() {
		tick := time.Tick(10 * time.Second)
		for {
			s, err := getStatus()
			if err != nil {
				log.Println(err)
			}
			ch <- s
			<-tick
		}
	}()

	var err error
	for fn := wait; fn != nil; {
		fn, err = fn(<-ch)
		if err != nil {
			log.Println(err)
		}
	}
}
