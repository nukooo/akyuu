package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nukooo/log"
)

type status struct {
	live     bool
	dj, song string
}

func getStatus() (status, error) {
	r, err := http.Get("https://r-a-d.io/api")
	if err != nil {
		return status{}, err
	}
	defer r.Body.Close()
	var v struct {
		Main struct {
			IsAFKStream bool
			DJ          struct{ DJName string }
			NP          string
		}
	}
	err = json.NewDecoder(r.Body).Decode(&v)
	if err != nil {
		return status{}, err
	}
	return status{!v.Main.IsAFKStream, v.Main.DJ.DJName, v.Main.NP}, nil
}

func getStream() (io.ReadCloser, error) {
	resp, err := http.Get("https://stream.r-a-d.io/main.mp3")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

type stateFunc func(status) stateFunc

func wait(s status) stateFunc {
	if s.live {
		return record(s)
	}
	return wait
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

func record(s status) stateFunc {
	log.Println(s.dj, "is streaming")
	if hook != nil {
		go hook("record", s.dj)
	}

	basename := time.Now().Format(time.RFC3339) + "-" + s.dj
	filename := basename + ".mp3"
	audio, err := os.Create(filename)
	if err != nil {
		log.Fatalf("error creating %q: %v\n", filename, err)
	}
	filename = basename + ".cue"
	cue, err := os.Create(filename)
	if err != nil {
		audio.Close()
		log.Fatalf("error creating %q: %v\n", filename, err)
	}

	cancel := make(chan struct{})
	go func() {
		for {
			stream, err := getStream()
			if err != nil {
				log.Println("failed to get stream:", err)
			} else {
				_, err = io.Copy(audio, &cancelReader{stream, cancel})
				if err != nil {
					log.Println("error copying stream:", err)
				}
				stream.Close()
			}

			// retry
			select {
			case <-cancel:
				audio.Close()
				return
			default:
			}
		}
	}()

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
	fn = func(s status) stateFunc {
		if s.dj != _s.dj {
			log.Println(_s.dj, "finished streaming")
			close(cancel)
			cue.Close()
			if s.live {
				return record(s)
			}
			if hook != nil {
				go hook("done")
			}
			return wait
		}
		if s.song != _s.song {
			writeCueTrack(s.song)
			_s.song = s.song
		}
		return fn
	}

	return fn
}

type multiWriter struct {
	writers []io.Writer
}

func (mw *multiWriter) Write(p []byte) (n int, err error) {
	for _, w := range mw.writers {
		w.Write(p)
	}
	return len(p), nil
}

func main() {
	var hookpath, logpath, outpath string
	flag.StringVar(&hookpath, "hook", "", "path to hook script")
	flag.StringVar(&logpath, "log", "", "path to log directory")
	flag.StringVar(&outpath, "out", ".", "path to output directory")
	flag.Parse()

	if logpath != "" {
		filename := time.Now().Format(time.RFC3339) + ".log"
		logfile, err := os.Create(filepath.Join(logpath, filename))
		if err != nil {
			log.Fatalf("error creating %q: %v\n", filename, err)
		}
		defer logfile.Close()
		log.SetOutput(&multiWriter{[]io.Writer{log.Writer(), logfile}})
		log.Printf("using log directory %q\n", logpath)
	}

	if hookpath != "" {
		hook = func(args ...string) {
			err := exec.Command(hookpath, args...).Run()
			if err != nil {
				log.Println("failed to run hook script:", err)
			}
		}
		log.Printf("using hook script %q\n", hookpath)
	}

	err := os.Chdir(outpath)
	if err != nil {
		log.Fatalf("failed to change to output directory %q: %v\n", err)
	}
	log.Printf("using output directory %q\n", outpath)

	log.Println("starting akyuu")

	ch := make(chan status)
	go func() {
		tick := time.Tick(10 * time.Second)
		for {
			s, err := getStatus()
			if err != nil {
				log.Println("failed to get status:", err)
			} else {
				ch <- s
			}
			<-tick
		}
	}()

	for fn := wait; fn != nil; fn = fn(<-ch) {
	}
}
