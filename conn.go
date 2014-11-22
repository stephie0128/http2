package http2

import (
	"fmt"
	. "github.com/Jxck/color"
	"github.com/Jxck/hpack"
	. "github.com/Jxck/http2/frame"
	. "github.com/Jxck/logger"
	"io"
	"log"
)

func init() {
	log.SetFlags(log.Lshortfile)
}

type Conn struct {
	RW           io.ReadWriter
	HpackContext *hpack.Context
	LastStreamID uint32
	Window       *Window
	Settings     map[SettingsID]int32
	PeerSettings map[SettingsID]int32
	Streams      map[uint32]*Stream
	WriteChan    chan Frame
	CallBack     func(stream *Stream)
}

func NewConn(rw io.ReadWriter) *Conn {
	conn := &Conn{
		RW:           rw,
		HpackContext: hpack.NewContext(uint32(DEFAULT_HEADER_TABLE_SIZE)),
		Settings:     DefaultSettings,
		PeerSettings: DefaultSettings,
		Window:       NewWindowDefault(),
		Streams:      make(map[uint32]*Stream),
		WriteChan:    make(chan Frame),
	}
	return conn
}

func (conn *Conn) NewStream(streamid uint32) *Stream {
	stream := NewStream(
		streamid,
		conn.WriteChan,
		conn.Settings,
		conn.PeerSettings,
		conn.HpackContext,
		conn.CallBack,
	)
	Debug("adding new stream (id=%d) total (%d)", stream.ID, len(conn.Streams))
	return stream
}

func (conn *Conn) HandleSettings(settingsFrame *SettingsFrame) {
	Debug("conn.HandleSettings(%v)", settingsFrame)

	if settingsFrame.Flags == ACK {
		// receive ACK
		Trace("receive SETTINGS ACK")
		return
	}

	if settingsFrame.Flags != UNSET {
		Error("unknown flag of SETTINGS Frame %v", settingsFrame.Flags)
		return
	}

	// save SETTINGS Frame
	settings := settingsFrame.Settings
	conn.Settings = settings

	// SETTINGS_INITIAL_WINDOW_SIZE
	initialWindowSize, ok := settings[SETTINGS_INITIAL_WINDOW_SIZE]
	if ok {
		if initialWindowSize > 65535 { // validate
			Error("FLOW_CONTROL_ERROR (%s)", "SETTINGS_INITIAL_WINDOW_SIZE too large")
			return
		}

		conn.Window.PeerCurrentSize -= conn.Window.InitialSize
		conn.Window.PeerCurrentSize += initialWindowSize
		conn.Window.InitialSize = initialWindowSize
		conn.PeerSettings[SETTINGS_INITIAL_WINDOW_SIZE] = initialWindowSize

		for _, stream := range conn.Streams {
			log.Println("apply settings to stream", stream)
			stream.Window.PeerCurrentSize -= stream.Window.InitialSize
			stream.Window.PeerCurrentSize += initialWindowSize
			stream.Window.InitialSize = initialWindowSize
			stream.PeerSettings[SETTINGS_INITIAL_WINDOW_SIZE] = initialWindowSize
		}
	}

	// send ACK
	ack := NewSettingsFrame(ACK, 0, NilSettings)
	conn.WriteChan <- ack
}

func (conn *Conn) ReadLoop() {
	Debug("start conn.ReadLoop()")
	for {
		// コネクションからフレームを読み込む
		frame, err := ReadFrame(conn.RW)
		if err != nil {
			if err == io.EOF {
				Error("%v", err)
				break
			}
			Fatal("%v", err)
		}
		if frame != nil {
			Notice("%v %v", Green("recv"), util.Indent(frame.String()))
		}

		// SETTINGS frame なら apply setting
		if frame.Header().Type == SettingsFrameType {
			settingsFrame, ok := frame.(*SettingsFrame)
			if !ok {
				Error("invalid settings frame %v", frame)
				return
			}
			conn.HandleSettings(settingsFrame)
		}

		// Connection Level Window Update
		if frame.Header().StreamID == 0 && frame.Header().Type == WindowUpdateFrameType {
			windowUpdateFrame, ok := frame.(*WindowUpdateFrame)
			if !ok {
				Error("invalid window update frame %v", frame)
				return
			}
			conn.Window.PeerCurrentSize += int32(windowUpdateFrame.WindowSizeIncrement)
		}

		// handle GOAWAY with close connection
		if frame.Header().Type == GoAwayFrameType {
			Debug("stop conn.ReadLoop() by GOAWAY")
			conn.Close()
			break
		}

		// DATA frame なら winodw update
		if frame.Header().Type == DataFrameType {
			length := int32(frame.Header().Length)
			conn.WindowUpdate(length)
		}

		// 以下 stream leve のコントロール
		// StreamID == 0 は無視
		streamID := frame.Header().StreamID
		if streamID == 0 {
			continue
		}

		// 新しいストリーム ID なら対応するストリームを生成
		stream, ok := conn.Streams[streamID]
		if !ok {
			// create stream with streamID
			stream = conn.NewStream(streamID)
			conn.Streams[streamID] = stream

			// update last stream id
			if streamID > conn.LastStreamID {
				conn.LastStreamID = streamID
			}
		}

		// stream の state を変える
		err = stream.ChangeState(frame, RECV)
		if err != nil {
			Error(Red(err))
		}

		// stream が close ならリストから消す
		if stream.State == CLOSED {
			Info("remove stream(%d) from conn.Streams[]", streamID)
			conn.Streams[streamID] = nil
		}

		// ストリームにフレームを渡す
		stream.ReadChan <- frame
	}
}

func (conn *Conn) WriteLoop() (err error) {
	Debug("start conn.WriteLoop()")
	for frame := range conn.WriteChan {
		Notice("%v %v", Red("send"), util.Indent(frame.String()))

		// TODO: ここで WindowSize を見る
		err = frame.Write(conn.RW)
		if err != nil {
			Error("%v", err)
			return err
		}
	}
	return
}

func (conn *Conn) WindowUpdate(length int32) {
	Debug("connection window update %d byte", length)

	conn.Window.CurrentSize = conn.Window.CurrentSize - length

	// この値を下回ったら WindowUpdate を送る
	if conn.Window.CurrentSize < conn.Window.Threshold {
		update := conn.Window.InitialSize - conn.Window.CurrentSize
		conn.WriteChan <- NewWindowUpdateFrame(0, uint32(update))
		conn.Window.CurrentSize = conn.Window.CurrentSize + update
	}
}

func (conn *Conn) WriteMagic() (err error) {
	_, err = conn.RW.Write([]byte(CONNECTION_PREFACE))
	if err != nil {
		return err
	}
	Info("%v %q", Red("send"), CONNECTION_PREFACE)
	return
}

func (conn *Conn) ReadMagic() (err error) {
	magic := make([]byte, len(CONNECTION_PREFACE))
	_, err = conn.RW.Read(magic)
	if err != nil {
		return err
	}
	if string(magic) != CONNECTION_PREFACE {
		Error("Invalid Magic String")
		return fmt.Errorf("Invalid Magic String")
	}
	Info("%v %q", Red("recv"), string(magic))
	return
}

func (conn *Conn) Close() {
	Info("close all conn.Streams")
	for i, stream := range conn.Streams {
		if stream != nil {
			Debug("close stream(%d)", i)
			stream.Close()
		}
	}
	Info("close conn.WriteChan")
	close(conn.WriteChan)
}
