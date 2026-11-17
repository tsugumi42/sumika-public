param([Parameter(Mandatory)][string]$Dir)
# b2-09-go-cli 的参考做法：在 $Dir 里补 count、top、uniq 三个子命令，每个做完发布一次
$ErrorActionPreference = 'Stop'
$enc = New-Object System.Text.UTF8Encoding($false)
Push-Location $Dir
try {
    $count = @'
package main

import (
	"fmt"
	"io"
	"strings"
)

func runCount(args []string, in io.Reader, out io.Writer) error {
	fs := newFlags("count")
	l := fs.Bool("l", false, "")
	w := fs.Bool("w", false, "")
	c := fs.Bool("c", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := readInput(fs, in)
	if err != nil {
		return err
	}
	all := !*l && !*w && !*c
	var parts []string
	if all || *l {
		parts = append(parts, fmt.Sprintf("lines=%d", countLines(data)))
	}
	if all || *w {
		parts = append(parts, fmt.Sprintf("words=%d", len(strings.Fields(string(data)))))
	}
	if all || *c {
		parts = append(parts, fmt.Sprintf("bytes=%d", len(data)))
	}
	_, err = fmt.Fprintln(out, strings.Join(parts, " "))
	return err
}
'@
    [IO.File]::WriteAllText((Join-Path $Dir 'cmd_count.go'), $count, $enc)
    $main = [IO.File]::ReadAllText((Join-Path $Dir 'main.go'))
    $main = $main.Replace("`"lines`": runLines,", "`"lines`": runLines,`n`t`"count`": runCount,")
    [IO.File]::WriteAllText((Join-Path $Dir 'main.go'), $main, $enc)
    & (Join-Path $Dir 'tools\publish.ps1') -Name cmd-count

    $top = @'
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"unicode"
)

func runTop(args []string, in io.Reader, out io.Writer) error {
	flags := newFlags("top")
	n := flags.Int("n", 3, "")
	stop := flags.String("stop", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *n < 1 {
		return errors.New("-n 要大于等于 1")
	}
	var stopwords map[string]bool
	var err error
	if *stop != "" {
		if stopwords, err = loadStopwords(*stop); err != nil {
			return err
		}
	} else if stopwords, err = loadStopwords("config/stopwords.txt"); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		stopwords = nil
	}
	data, err := readInput(flags, in)
	if err != nil {
		return err
	}
	words := strings.FieldsFunc(strings.ToLower(string(data)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	counts := map[string]int{}
	for _, w := range words {
		if !stopwords[w] {
			counts[w]++
		}
	}
	list := make([]string, 0, len(counts))
	for w := range counts {
		list = append(list, w)
	}
	sort.Slice(list, func(i, j int) bool {
		if counts[list[i]] != counts[list[j]] {
			return counts[list[i]] > counts[list[j]]
		}
		return list[i] < list[j]
	})
	if len(list) > *n {
		list = list[:*n]
	}
	for _, w := range list {
		if _, err := fmt.Fprintf(out, "%s %d\n", w, counts[w]); err != nil {
			return err
		}
	}
	return nil
}
'@
    [IO.File]::WriteAllText((Join-Path $Dir 'cmd_top.go'), $top, $enc)
    $main = [IO.File]::ReadAllText((Join-Path $Dir 'main.go'))
    $main = $main.Replace("`"count`": runCount,", "`"count`": runCount,`n`t`"top`": runTop,")
    [IO.File]::WriteAllText((Join-Path $Dir 'main.go'), $main, $enc)
    & (Join-Path $Dir 'tools\publish.ps1') -Name cmd-top

    $uniq = @'
package main

import (
	"fmt"
	"io"
	"strings"
)

func runUniq(args []string, in io.Reader, out io.Writer) error {
	fs := newFlags("uniq")
	c := fs.Bool("c", false, "")
	i := fs.Bool("i", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := readInput(fs, in)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	same := func(a, b string) bool {
		if *i {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	flush := func(line string, n int) error {
		if *c {
			line = formatCount(n, line)
		}
		_, err := fmt.Fprintln(out, line)
		return err
	}
	cur, n := lines[0], 1
	for _, l := range lines[1:] {
		if same(cur, l) {
			n++
			continue
		}
		if err := flush(cur, n); err != nil {
			return err
		}
		cur, n = l, 1
	}
	return flush(cur, n)
}
'@
    [IO.File]::WriteAllText((Join-Path $Dir 'cmd_uniq.go'), $uniq, $enc)
    $main = [IO.File]::ReadAllText((Join-Path $Dir 'main.go'))
    $main = $main.Replace("`"top`": runTop,", "`"top`": runTop,`n`t`"uniq`": runUniq,")
    [IO.File]::WriteAllText((Join-Path $Dir 'main.go'), $main, $enc)

    gofmt -l . | Out-Null
    go test ./... 2>&1 | Write-Host
    if ($LASTEXITCODE -ne 0) { throw 'go test 没过' }
    & (Join-Path $Dir 'tools\publish.ps1') -Name cmd-uniq
}
finally { Pop-Location }
