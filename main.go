package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type status struct{ dj, song string }

func getStatus() (status, error) {
	r, err := http.Get("https://r-a-d.io/api")
	if err != nil {
		return status{}, err
	}
	defer r.Body.Close()
	var v struct {
		Main struct {
			DJ struct{ DJName string }
			NP string
		}
	}
	err = json.NewDecoder(r.Body).Decode(&v)
	if err != nil {
		return status{}, err
	}
	return status{v.Main.DJ.DJName, v.Main.NP}, nil
}

func getStream() (io.ReadCloser, error) {
	resp, err := http.Get("https://stream.r-a-d.io/main.mp3")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

type stateFunc func(status) (stateFunc, error)

func isLive(dj string) bool {
	return dj != "" && dj != "Hanyuu-sama"
}

func wait(s status) (stateFunc, error) {
	if isLive(s.dj) {
		return record(s)
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

func record(s status) (stateFunc, error) {
	go hook("record", s.dj)

	stream, err := getStream()
	if err != nil {
		return wait, err
	}

	basename := time.Now().Format(time.RFC3339) + "-" + s.dj
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
	go io.Copy(audio, &cancelReader{stream, cancel})

	fmt.Fprintf(cue, "FILE %q MP3\n", basename+".mp3")
	start := time.Now()
	var track int = 1
	writeCueTrack := func(song string) {
		index := time.Since(start)
		fmt.Fprintf(cue, "  TRACK %02d AUDIO\n    TITLE %q\n    INDEX 01 %02.f:%02.f:%02.f\n",
			track,
			song,
			index.Minutes(),
			(index % time.Minute).Seconds(),
			(index%time.Second).Seconds()*75)
		track++
	}
	writeCueTrack(s.song)

	_s := s
	var fn stateFunc
	fn = func(s status) (stateFunc, error) {
		if s.dj != _s.dj {
			close(cancel)
			cue.Close()
			audio.Close()
			stream.Close()
			if isLive(s.dj) {
				return record(s)
			}
			go hook("done")
			return wait, nil
		}
		if s.song != _s.song {
			writeCueTrack(s.song)
			_s.song = s.song
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
