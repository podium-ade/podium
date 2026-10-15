//go:build sandbox

package sandboxtest

import "testing"

func TestLocalConformance(t *testing.T) {
	Run(t, &Local{Base: "python:3-alpine"}, Options{ServeCmd: "python3 -m http.server --bind %s %d"})
}

func TestLocalDockerConformance(t *testing.T) {
	// The dind image has no web server of its own, so the page is served by a container on
	// the sandbox's own engine, which is also the proof that the engine works.
	Run(t, &Local{Docker: true}, Options{ServeCmd: `docker run --rm -p %s:%d:80 -v "$PWD":/www busybox:1 httpd -f -h /www`})
}
