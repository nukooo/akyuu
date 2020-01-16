package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/nukooo/log"
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
	log.Println(s.dj, "is streaming")
	if hook != nil {
		go hook("record", s.dj)
	}

	stream, err := getStream()
	if err != nil {
		return wait, fmt.Errorf("failed to get stream: %w", err)
	}

	basename := time.Now().Format(time.RFC3339) + "-" + s.dj
	filename := basename + ".mp3"
	audio, err := os.Create(filename)
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("error creating %q: %w", filename, err)
	}
	filename = basename + ".cue"
	cue, err := os.Create(filename)
	if err != nil {
		audio.Close()
		stream.Close()
		return nil, fmt.Errorf("error creating %q: %w", filename, err)
	}

	cancel := make(chan struct{})
	go io.Copy(audio, &cancelReader{stream, cancel})

	fmt.Fprintf(cue, "FILE %q MP3\n", basename+".mp3")
	start := time.Now()
	var track int = 1
	writeCueTrack := func(song string) {
		index := time.Since(start)
		fmt.Fprintf(cue, "  TRACK %02d AUDIO\n    TITLE \"%s\"\n    INDEX 01 %02d:%02d:%02d\n",
			track,
			song,
			index/time.Minute,
			index%time.Minute/time.Second,
			index%time.Second*75/time.Second)
		track++
	}
	writeCueTrack(s.song)

	_s := s
	var fn stateFunc
	fn = func(s status) (stateFunc, error) {
		if s.dj != _s.dj {
			log.Println(_s.dj, "finished streaming")
			close(cancel)
			cue.Close()
			audio.Close()
			stream.Close()
			if isLive(s.dj) {
				return record(s)
			}
			if hook != nil {
				go hook("done")
			}
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

	log.Println("starting akyuu")

	if hookpath != "" {
		hook = func(args ...string) {
			err := exec.Command(hookpath, args...).Run()
			if err != nil {
				log.Println("failed to run hook script:", err)
			}
		}
		log.Printf("using hook script %q\n", hookpath)
	}

	ch := make(chan status)
	go func() {
		tick := time.Tick(10 * time.Second)
		for {
			s, err := getStatus()
			if err != nil {
				log.Println("failed to get status:", err)
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
