package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"sync"
)

type frame struct {
	Number  int     `json:"number"`
	Total   float64 `json:"totalMs"`
	Build   float64 `json:"buildMs"`
	Layout  float64 `json:"layoutMs"`
	Paint   float64 `json:"paintMs"`
	Present float64 `json:"presentMs"`
}

var framePattern = regexp.MustCompile(`mygo: frame (\d+) took ([\d.]+) ms: build ([\d.]+).*?, layout ([\d.]+), paint ([\d.]+), present ([\d.]+)`)

type journal struct {
	mu     sync.Mutex
	frames []frame
	file   *os.File
	err    error
}

func (j *journal) copy(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		j.mu.Lock()
		if _, err := fmt.Fprintln(j.file, line); err != nil && j.err == nil {
			j.err = err
		}
		if m := framePattern.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			var nums [5]float64
			for i := range nums {
				nums[i], _ = strconv.ParseFloat(m[i+2], 64)
			}
			j.frames = append(j.frames, frame{n, nums[0], nums[1], nums[2], nums[3], nums[4]})
		}
		j.mu.Unlock()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := sc.Err(); err != nil && j.err == nil {
		j.err = err
	}
}
func (j *journal) position() int { j.mu.Lock(); defer j.mu.Unlock(); return len(j.frames) }
func (j *journal) since(start int) []frame {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := len(j.frames)
	return j.frames[start:end:end]
}
