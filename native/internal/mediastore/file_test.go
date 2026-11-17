package mediastore

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestFileKindRequiresMatchingContent(t *testing.T) {
	var doc bytes.Buffer
	z := zip.NewWriter(&doc)
	for _, name := range []string{"[Content_Types].xml", "word/document.xml"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("<document/>")); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		head []byte
		want string
	}{
		{"清单.TXT", []byte("hello\n编造文本"), "text"},
		{"文档.pdf", []byte("%PDF-1.7\n"), "document"},
		{"文档.docx", doc.Bytes(), "document"},
		{"表格.xlsx", doc.Bytes(), ""},
		{"文档.docx", []byte("PK\x03\x04not-a-document"), ""},
		{"动画.gif", []byte("GIF89a12345"), "image"},
		{"图片.jpg", []byte("GIF89a12345"), ""},
		{"录音.flac", []byte("fLaC1234"), "audio"},
		{"录音.mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), "audio"},
		{"影片.mp4", []byte("\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isommp42"), "video"},
		{"影片.mov", []byte("\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isommp42"), ""},
		{"无后缀", []byte("hello"), ""},
		{"程序.exe", []byte("MZ1234"), ""},
		{"假图.png", []byte("MZ1234"), ""},
		{"假文本.txt", []byte("MZ\x00\x00\x01"), ""},
		{"假文本.txt", []byte("#!/bin/sh\necho hi"), ""},
		{"脚本.ps1", []byte("Write-Output hello"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileKind(tc.name, tc.head); got != tc.want {
				t.Fatalf("kind=%q want=%q", got, tc.want)
			}
		})
	}
}
