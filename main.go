package main

import (
	//	"bufio"
	//	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/kr/pty"
	"golang.org/x/net/websocket"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

type (
	Api struct {
		PermitWrite bool
		Command     []string
	}
	Client struct {
		api     *Api
		conn    *websocket.Conn
		command *exec.Cmd
		pty     *os.File
	}
	argResizeTerminal struct {
		Columns float64
		Rows    float64
	}
)

const (
	Input          = '0'
	Ping           = '1'
	ResizeTerminal = '2'
)

const (
	Output         = '0'
	Pong           = '1'
	SetWindowTitle = '2'
	SetPreferences = '3'
	SetReconnect   = '4'
)

var (
	port        int
	auth        string
	PermitWrite bool
)

func (c *Client) processSend() {

	for {
		buf := make([]byte, 1024)
		size, err := c.pty.Read(buf)
		if err != nil {
			log.Printf("Command exited for: %s %s", c.conn.RemoteAddr, err)
			return
		}
		safeMessage := base64.StdEncoding.EncodeToString([]byte(buf[:size]))
		_, err = c.conn.Write(append([]byte{Output}, []byte(safeMessage)...))
		if err != nil {
			log.Printf("write err:%s", err.Error())
			return
		}
	}
}
func (c *Client) processReceive() {
	for {
		var data = make([]byte, 1024)
		size, err := c.conn.Read(data)
		if err != nil {
			log.Print(err.Error())
			return
		}
		if len(data) == 0 {
			log.Print("An error has occured")
			return
		}
		switch data[0] {
		case Input:
			if !c.api.PermitWrite {
				break
			}
			_, err := c.pty.Write(data[1:size])
			if err != nil {
				return
			}
		case Ping:
			_, err := c.conn.Write([]byte{Pong})
			if err != nil {
				log.Print(err.Error())
				return
			}
		case ResizeTerminal:
			var args argResizeTerminal
			err = json.Unmarshal(data[1:size], &args)
			if err != nil {
				log.Print("Malformed remote command %s %s", err, data[:size])
				return
			}
			window := struct {
				row uint16
				col uint16
				x   uint16
				y   uint16
			}{
				uint16(args.Rows),
				uint16(args.Columns),
				0,
				0,
			}
			syscall.Syscall(
				syscall.SYS_IOCTL,
				c.pty.Fd(),
				syscall.TIOCSWINSZ,
				uintptr(unsafe.Pointer(&window)),
			)
		default:
			log.Print("Unknown message type")
			return
		}
	}
}

func (a *Api) ExecCommand(ws *websocket.Conn) {
	cmd := exec.Command(a.Command[0], a.Command[1:]...)
	ptyIo, err := pty.Start(cmd)
	if err != nil {
		log.Errorf("Start %v %s", a.Command, err)
	}
	c := Client{api: a, conn: ws, command: cmd, pty: ptyIo}
	exit := make(chan bool, 2)
	go func() {
		c.processReceive()
		exit <- true
	}()
	go func() {
		c.processSend()
		exit <- true
	}()
	<-exit
	ptyIo.Close()
	cmd.Process.Signal(syscall.Signal(1))
	cmd.Wait()
	ws.Close()
}
func init() {
	flag.IntVar(&port, "port", 8080, "port ")
	flag.IntVar(&port, "p", 8080, "port ")
	flag.StringVar(&auth, "auth", "root:root", "auth ")
	flag.StringVar(&auth, "a", "root:root", "auth ")
	flag.BoolVar(&PermitWrite, "write", false, "write")
	flag.BoolVar(&PermitWrite, "w", false, "write")
}

func genArgs() []string {
	usage := os.Args[0] + " [options] run command...\n" + `
options:
--auth , -a	Credential for Basic Authentication (ex: user:pass, default root:root)
--port, -p	Port number to listen default 8080
--write, -w	Permit clients to write to the TTY (BE CAREFUL)
	`
	args := []string{}
	c := 0
	for i, v := range os.Args[1:] {
		if v != "run" {
			args = append(args, v)
		} else if c == 0 {
			c = i + 2
			log.Printf("index %d ", c)
		}
	}
	if len(args) == len(os.Args[1:]) || c >= len(os.Args) {
		log.Errorf("usage:%s", usage)
		os.Exit(-1)
	}
	flag.CommandLine.Parse(args)
	log.Printf("%s %d %s", auth, port, PermitWrite)
	return os.Args[c:]
}

func wrapBasicAuth(handler http.Handler, credential string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if u+":"+p != credential || !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="GoTTY"`)
			http.Error(w, "Bad Request", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func main() {
	cmd := genArgs()
	log.Printf("run %v\n", cmd)
	a := &Api{PermitWrite: PermitWrite, Command: cmd}
	globalMux := http.NewServeMux()
	globalMux.Handle("/", wrapBasicAuth(http.FileServer(http.Dir("static")), auth))
	globalMux.Handle("/ws", wrapBasicAuth(websocket.Handler(a.ExecCommand), auth))
	s := &http.Server{
		Addr:    "0.0.0.0:" + fmt.Sprintf("%d", port),
		Handler: globalMux,
	}
	var runErr error
	runErr = s.ListenAndServe()
	if runErr != nil {
		log.Fatal(runErr)
	}

}
