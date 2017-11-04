For a number of years I had one of these cheap electronic safes.  They allow
for a combination to be set.

But it broke, so I worked out how to create an Arduino replacement.

The details of the safe and how I built it is at https://www.sweharris.org/post/2017-10-09-digital_safe/

This repository contains the source for the Arduino sketch and a simple
web interface written in `go`

You can't `go build` this repo 'cos we have more than just the web server;
you can `git clone` it, and there's a sample "build.sh" script that
sets `GOPATH` as necesary.


GPL2 or later
