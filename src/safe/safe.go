// we use github.com/tarm/goserial
//        github.com/tkanos/gonfig
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/tarm/goserial"
	"github.com/tkanos/gonfig"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Global debug flag
var DEBUG bool = false

// Global variables for the port we have open and the associated
// reader buffer
var port io.ReadWriteCloser
var reader *bufio.Reader

// What characters we allow for safe passwords.  In theory anything except
// a : should work, but we're gonna be more restrictive
const pswdstring = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

var validPsw = regexp.MustCompile("^[" + pswdstring + "]+$").MatchString

// What values we want to read from our config file
type Configuration struct {
	SerialPort string // Where the safe is connected (OS dependent default)
	ListenPort int    // What TCP port to listen on (default 5000)
	AuthUser   string // If set, require this username
	AuthPass   string // Require this password
	HTMLDir    string // Where the HTML static directory is (autoset)
}

var configuration Configuration

// This will display output if the environment variable DEBUG
// is set to "1"
//  eg  DEBUG=1 ./safe

func debug(msg string) {
	if DEBUG {
		fmt.Println(msg)
	}
}

//////////////////////////////////////////////////////////////////////
//
// This functions kinda stolen from path.go and kludged because
// it didn't work on Windows go/1.9.1
//
//////////////////////////////////////////////////////////////////////

func Dir(p string) string {
        i := strings.LastIndex(p, string(os.PathSeparator))
        return p[:i+1]
}

//////////////////////////////////////////////////////////////////////
//
// JPEG file handling
//
//////////////////////////////////////////////////////////////////////

// These are the fields we care about keeping
type JPEG struct {
	dqt      [10][]byte
	comment  []byte
	sof0     []byte
	dht      [10][]byte
	sos      []byte
	img      []byte
	dqtcount int
	dhtcount int
}

var lock_image JPEG

func read_jpeg_segment(img []byte, offset int) (int, int, []byte, error) {
	var segment int
	var size int
	var res []byte
	if img[offset] != 0xff {
		return 0,0,nil,errors.New("Bad JPEG - expected 0xff at " + strconv.Itoa(offset))
	}
	segment = int(img[offset+1])
	size = int(img[offset+2])*256 + int(img[offset+3])
	res = img[offset+4 : offset+4+size-2]
	return segment, size, res, nil
}

func write_jpeg_segment(f io.Writer, marker int, data []byte) {
	var buf [4]byte
	l := len(data) + 2
	buf[0] = 0xff
	buf[1] = byte(marker)
	buf[2] = byte(l >> 8)
	buf[3] = byte(l & 255)
	f.Write(buf[:4])
	f.Write(data)
}

func parse_jpeg(img []byte) (JPEG, error) {
	var image JPEG

	if img[0] != 0xff && img[1] != 0xd8 {
		return image, errors.New("Image is not a JPEG - bad header")
	}
	if img[len(img)-2] != 0xff && img[len(img)-1] != 0xd9 {
		return image, errors.New("Image is not a JPEG - bad footer")
	}
	offset := 2

	for {
		section, size, data, err := read_jpeg_segment(img, offset)
		if err != nil {
			return image, err
		}
		offset += size + 2
		if section == 0xfe {
			image.comment = data
		} else if section == 0xc0 {
			image.sof0 = data
		} else if section == 0xda {
			image.sos = data
			break
		} else if section == 0xdb {
			image.dqt[image.dqtcount] = data
			image.dqtcount++
			if image.dqtcount > 9 {
				return image, errors.New("Too many DQT segments")
			}
		} else if section == 0xc4 {
			image.dht[image.dhtcount] = data
			image.dhtcount++
			if image.dhtcount > 9 {
				return image, errors.New("Too many DHT segments")
			}
		}
	}
	image.img = img[offset : len(img)-2]

	return image, nil
}

func read_jpeg(filename string) (JPEG, error) {
	img, err := ioutil.ReadFile(filename)
	if err != nil {
		return lock_image,errors.New("Could not open file " + filename)
	}
	return parse_jpeg(img)
}

func write_jpeg(f io.Writer, image JPEG) {
	var head [2]byte
	head[0] = 0xff
	head[1] = 0xd8
	var foot [2]byte
	foot[0] = 0xff
	foot[1] = 0xd9

	f.Write(head[:2])
	write_jpeg_segment(f, 0xfe, image.comment)
	for i := 0; i < image.dqtcount; i++ {
		write_jpeg_segment(f, 0xdb, image.dqt[i])
	}
	write_jpeg_segment(f, 0xc0, image.sof0)
	for i := 0; i < image.dhtcount; i++ {
		write_jpeg_segment(f, 0xc4, image.dht[i])
	}
	write_jpeg_segment(f, 0xda, image.sos)
	f.Write(image.img)
	f.Write(foot[:2])
}

//////////////////////////////////////////////////////////////////////
//
// Functions for serial communication to the safe
//
//////////////////////////////////////////////////////////////////////

// Try and drain the input stream from any random data that might
// be sitting on the USB port.  We try to read from the port
// (which will timeout based on the ReadTimeout setting).  If we
// read something then wait a second and try again.  Keep on this
// loop until there's no data left to be read.  Eventually this
// should drain the input, unless something else is writing data
// to the safe and causing it to generate data!  Should not happen
// because Send() will call Drain() first.

func Drain() {
	buf := make([]byte, 128)
	for {
		debug("Discarding any rogue data")
		n, err := port.Read(buf)
		if err != nil || n == 0 {
			break
		}
		debug("Discarded " + strconv.Itoa(n) + " bytes")
		time.Sleep(time.Second * 1)
	}
	debug("Hopefully empty")
}

// Send a string to the safe.  Attempt to drain any input before
// hand because it won't be useful.

func Send(str string) (n int, err error) {
	Drain()
	debug("Sending " + str)
	return port.Write([]byte(str))
}

// Read a string from the safe, and hope it ends in a LF character.
// We strip off CR/LF from the result.
// error is set if no LF received within the port timeout period

func Read() (str string, err error) {
	reply, err := reader.ReadString('\x0a')

	if err != nil {
		debug("No LF received: " + err.Error())
	}
	reply = strings.TrimRight(reply, "\r\n")
	debug("Received " + reply)

	return reply, err
}

// Send and read one line
func SendRead(str string) string {
	Send(str)
	time.Sleep(time.Second)
	res, _ := Read()
	return res
}

// Send and print result

func SendPrint(str string) {
	fmt.Println(SendRead(str))
}

// Try up to 5 times to send a PING message and get a PINGACK response.
// That indicates we've successfully communicated with the safe and
// there's no random data that might cause invalid responses left in
// the buffer
//
// Given how Send()/Drain() works, we shouldn't need to go more than once,
// but just in case...

func Sync() (string, bool) {
	connected := false
	for n := 1; n <= 5 && !connected; n++ {
		str := strconv.Itoa(rand.Int())
		Send(":ping:" + str + ":")
		debug("Attempt " + strconv.Itoa(n) + " connecting to safe")

		for !connected {
			x, err := Read()
			if strings.HasSuffix(x, "PINGACK:"+str+":") {
				debug("Successful PINGACK received")
				connected = true
			} else {
				debug("Discarding " + x)
			}
			if err != nil {
				// No data to be read; safe has stopped
				// talking to us
				break
			}
		}
	}
	if !connected {
		return "Failed to connect to safe", false
	}
	return "Connected to safe", true
}

//////////////////////////////////////////////////////////////////////
//
// Web server functionality
//
//////////////////////////////////////////////////////////////////////

func http_once(w http.ResponseWriter, request string) string {
	res := SendRead(request)
	fmt.Fprintln(w, res+"<br>")
	return res
}

func http_open(w http.ResponseWriter, durstr string) {
	if durstr == "" {
		durstr = "5"
	}

	fw, _ := w.(http.Flusher)

	// This causes chrome to render progressively
	// Magic numbers...
	// https://stackoverflow.com/questions/16909227/using-transfer-encoding-chunked-how-much-data-must-be-sent-before-browsers-s
	fmt.Fprintln(w, "<!--"+strings.Repeat(" ", 4096)+"-->")
	fw.Flush()

	res := http_once(w, ":open:"+durstr+":")

	if strings.HasPrefix(res, "OK") {
		debug("Looping on input")
		for res != "OK completed" {
			fw.Flush()
			res, _ = Read()
			if res != "" {
				fmt.Fprintln(w, res+"<br>")
			}
		}
	}
	debug("Loop done")
}

func check_psw(w http.ResponseWriter, psw1 string) bool {
	if psw1 == "" {
		fmt.Fprintln(w, "ERROR Missing password")
		return false
	} else if !validPsw(psw1) {
		fmt.Fprintln(w, "ERROR Password contains invalid characters.<br>Letters and numbers only")
		return false
	}
	return true
}

func http_lock(w http.ResponseWriter, psw1 string, psw2 string) {
	if psw1 != psw2 {
		fmt.Println(w, "ERROR Passwords don't match")
	} else if check_psw(w, psw1) {
		fmt.Fprintln(w, "Setting password: ")
		http_once(w, ":lock:"+psw1+":")
		fmt.Fprintln(w, "Testing password: ")
		http_once(w, ":test:"+psw1+":")
	}
}

func http_unlock_file(w http.ResponseWriter, cmd string, file io.Reader) {
	if file == nil {
		fmt.Fprintln(w, "No file selected")
		return
	}
	buf := new(bytes.Buffer)
	io.Copy(buf, file)
	img, err := parse_jpeg(buf.Bytes())
	if err != nil {
		fmt.Fprintln(w, "Could not parse JPEG file: " + err.Error())
		return
	}
	psw := string(img.comment)
	if strings.HasPrefix(psw,"LOCKPSW:") {
		psw = psw[8:]
		if check_psw(w, psw) {
			http_once(w, ":"+cmd+":"+psw+":")
		}
	} else {
		fmt.Fprintln(w, "This is not a valid password image")
	}
}

func http_unlock(w http.ResponseWriter, cmd string, psw1 string) {
	if check_psw(w, psw1) {
		http_once(w, ":"+cmd+":"+psw1+":")
	}
}

func http_random(w http.ResponseWriter) {
	b := make([]byte, 30)
	for i := range b {
		b[i] = pswdstring[rand.Intn(len(pswdstring))]
	}
	pswd := string(b)
	// DEBUG
	// pswd = "hello"

	res := SendRead(":lock:"+pswd+":")
	if !strings.HasPrefix(res,"OK") {
		fmt.Fprintln(w, "Error setting password: "+res)
		return
	}
	res = SendRead(":test:"+pswd+":")
	if !strings.HasPrefix(res,"OK") {
		fmt.Fprintln(w, "Error testing password: "+res)
		fmt.Fprintln(w, "We tried to set it to: "+pswd)
		return
	}
        lock_image.comment = []byte("LOCKPSW:" +pswd)

	// What filename should we save this as
	t := time.Now()
	fname := t.Format("20060102-150405")
	
	// Change results to be binary
	w.Header().Set("Content-Type", "binary/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="safe-` + fname + `.jpg"`)
	write_jpeg(w,lock_image)
}

func webserv(w http.ResponseWriter, r *http.Request) {
	// Ensure the connection is closed
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Type", "text/html")

	// Read the requested form - no more than 1 Meg
	var err error
	if r.Method == "GET" {
		err = r.ParseForm()
	} else {
		err = r.ParseMultipartForm(1024*1024)
	}

	if err != nil {
		fmt.Fprintln(w, err.Error())
		return
	}

	if r.FormValue("status") != "" {
		http_once(w, ":status::")
	} else if r.FormValue("open") != "" {
		http_open(w, r.FormValue("duration"))
	} else if r.FormValue("unlock_1") != "" {
		http_unlock(w, "unlock", r.FormValue("unlock"))
	} else if r.FormValue("unlock_all") != "" {
		http_unlock(w, "clear", r.FormValue("unlock"))
	} else if r.FormValue("pwtest") != "" {
		http_unlock(w, "test", r.FormValue("unlock"))
	} else if r.FormValue("lock") != "" {
		http_lock(w, r.FormValue("lock1"), r.FormValue("lock2"))
	} else if r.FormValue("random") != "" {
		http_random(w)
	} else if r.FormValue("image_test") != "" {
		file, _, _ := r.FormFile("fileToUpload")
		http_unlock_file(w, "test", file)
	} else if r.FormValue("image_unlock_1") != "" {
		file, _, _ := r.FormFile("fileToUpload")
		http_unlock_file(w, "unlock", file)
	} else if r.FormValue("image_unlock_all") != "" {
		file, _, _ := r.FormFile("fileToUpload")
		http_unlock_file(w, "clear", file)
	} else {
		fmt.Fprintln(w, "Unknown request<br>")
	}
}

// Where should we look for the configuration file?  This is in JSON
// format and looks like
//
// {
//   "ListenPort":12345,
//   "SerialPort":"/dev/ttyUSB4",
//   "AuthUser":"username",
//   "AuthPass":"pass",
//   "HTMLDir":"/opt/safe/static"
// }
//
// On Windows the SerialPort would be something like COM4
// On Linux it may make sense to use a more explicit path to the
// specific USB port, to avoid detection order issues; eg
//    /dev/serial/by-id/usb-1a86_USB2.0-Ser_-if00-port0
//
// ListenPort and SerialPort are required

func UserHomeDir() string {
	if runtime.GOOS == "windows" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		return home + "\\"
	}
	return os.Getenv("HOME") + "/"
}

func abort(str string) {
	fmt.Fprintln(os.Stderr, "\n"+str)
	os.Exit(-1)
}

func auth(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if configuration.AuthUser != "" {
			user, pass, _ := r.BasicAuth()
			if user != configuration.AuthUser ||
				pass != configuration.AuthPass {
				w.Header().Set("WWW-Authenticate", `Basic realm="Restricted safe"`)
				http.Error(w, "Unauthorized.", 401)
				return
			}
		}
		fn(w, r)
	}
}

func main() {
	DEBUG, _ = strconv.ParseBool(os.Getenv("DEBUG"))

	// Let's seed our random function
	rand.Seed(time.Now().UnixNano())

	// Try and find the config file
	config_file := UserHomeDir() + ".safe.cfg"
	fmt.Println("Using configuration file " + config_file)

	gonfig.GetConf(config_file, &configuration)

	// Set defaults, if not otherwise defined
	if configuration.ListenPort == 0 {
		configuration.ListenPort = 5000
	}

	if configuration.SerialPort == "" {
		if runtime.GOOS == "windows" {
			configuration.SerialPort = "COM1"
		} else {
			configuration.SerialPort = "/dev/ttyUSB0"
		}
	}

	// Where are we running from?
	if configuration.HTMLDir == "" {
		ex, _ := os.Executable()
		configuration.HTMLDir = Dir(ex) + "static"
	}

	fmt.Println("  Serial Port = " + configuration.SerialPort)
	fmt.Println("  Listen Port = " + strconv.Itoa(configuration.ListenPort))
	fmt.Println("  HTML static = " + configuration.HTMLDir)

	// Load the lock image
	var err error
	lock_image, err = read_jpeg(configuration.HTMLDir + "/lock_image.jpg")
	if err != nil {
		abort(err.Error())
	}
	fmt.Println("  Lock image loaded")

	config := &serial.Config{
		Name:        configuration.SerialPort,
		Baud:        9600,
		ReadTimeout: time.Millisecond * 100}
	p, err := serial.OpenPort(config)

	if err != nil {
		abort("Could not open serial port: " + err.Error())
	}
	port = p
	reader = bufio.NewReader(port)

	conn_msg, conn_ok := Sync()
	if !conn_ok {
		abort(conn_msg)
	}
	fmt.Println("  Successfully connected to Safe")
	fmt.Println("  " + SendRead(":status::"))

	// Set up the HTTP listener
	fs := http.FileServer(http.Dir(configuration.HTMLDir))
	http.HandleFunc("/safe/", auth(webserv))
	http.HandleFunc("/", auth(fs.ServeHTTP))

	err = http.ListenAndServe(":"+strconv.Itoa(configuration.ListenPort), nil)
	if err != nil {
		abort("ListenAndServe: " + err.Error())
	}
}
