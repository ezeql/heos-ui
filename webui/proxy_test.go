package webui

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeFFmpeg puts an ffmpeg on PATH that ignores its input and writes body.
func fakeFFmpeg(t *testing.T, body string, exit int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '" + body + "'\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The speaker sees an ordinary file: the promised length, the converted bytes
// first, byte ranges for seeking, and a HEAD that reads nothing.
func TestAConvertedTrackIsServedLikeAFile(t *testing.T) {
	fakeFFmpeg(t, "ABCDEFGHIJ", 0)
	server := New(nil, NewLibrary(filepath.Join(t.TempDir(), "playlists.json")), nil)
	t.Cleanup(server.stopProxy)
	id, err := server.startProxy("https://example.com/playlist.m3u8", 1)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)
	url := web.URL + "/stream/" + id + ".mp3"
	size := proxyBytesPerSecond // one second

	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != int64(size) ||
		response.Header.Get("Content-Type") != "audio/mpeg" {
		t.Errorf("GET = %d, length %d, %q", response.StatusCode, response.ContentLength, response.Header.Get("Content-Type"))
	}
	if len(body) != size || string(body[:10]) != "ABCDEFGHIJ" || !bytes.Equal(body[10:], make([]byte, size-10)) {
		t.Errorf("body: %d bytes, starts %q", len(body), body[:min(len(body), 12)])
	}

	request, _ := http.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("Range", "bytes=2-5")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusPartialContent || string(body) != "CDEF" {
		t.Errorf("range = %d %q", response.StatusCode, body)
	}

	response, err = http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != int64(size) {
		t.Errorf("HEAD = %d, length %d", response.StatusCode, response.ContentLength)
	}

	response, err = http.Get(web.URL + "/stream/someoneelse.mp3")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id = %d", response.StatusCode)
	}
}

// A conversion that dies must cut the connection: padding the rest with
// zeros would leave the speaker playing silence for the length of a set.
func TestAFailedConversionIsNotServedAsSilence(t *testing.T) {
	fakeFFmpeg(t, "AB", 1)
	server := New(nil, NewLibrary(filepath.Join(t.TempDir(), "playlists.json")), nil)
	t.Cleanup(server.stopProxy)
	id, err := server.startProxy("https://example.com/playlist.m3u8", 60)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)

	response, err := http.Get(web.URL + "/stream/" + id + ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err == nil || len(body) >= 60*proxyBytesPerSecond {
		t.Errorf("read %d bytes, err %v: want a short read", len(body), err)
	}
}

// Playing something else ends the previous conversion and frees its file.
func TestANewConversionReplacesTheLast(t *testing.T) {
	fakeFFmpeg(t, "AB", 0)
	server := New(nil, NewLibrary(filepath.Join(t.TempDir(), "playlists.json")), nil)
	t.Cleanup(server.stopProxy)
	if _, err := server.startProxy("https://example.com/one.m3u8", 10); err != nil {
		t.Fatal(err)
	}
	first := server.proxy.file.Name()
	if _, err := server.startProxy("https://example.com/two.m3u8", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("first conversion's file still there: %v", err)
	}
}
