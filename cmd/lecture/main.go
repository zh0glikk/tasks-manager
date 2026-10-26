// Command lecture transcribes a class recording on this machine and uploads
// the transcript to the site. Video stays where it is (YouTube, Drive, Zoom,
// Telegram); only the link and the text are stored.
//
// It needs ffmpeg and whisper-cli (whisper.cpp) installed, and yt-dlp for links.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const usage = `usage:
  lecture -url https://youtu.be/… [flags]        download the audio with yt-dlp
  lecture -file video.mp4 -date 2026-10-23 …     a local file (e.g. saved from Telegram Desktop)
  lecture -transcript transcript.json …          an existing whisper.cpp JSON, skips transcription

Uploads to $E2_URL with the key in $E2_KEY (needs write access).
Pass -out FILE to save the request instead of uploading.

flags:`

type segment struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

type lecture struct {
	EventID    *int64    `json:"event_id,omitempty"`
	CourseID   *int64    `json:"course_id,omitempty"`
	Date       string    `json:"date"`
	Title      string    `json:"title"`
	VideoURL   string    `json:"video_url"`
	DurationS  int       `json:"duration_s"`
	Transcript []segment `json:"transcript"`
}

func main() {
	home, _ := os.UserHomeDir()
	var (
		link       = flag.String("url", "", "video link (YouTube, Google Drive, Zoom…); also stored as the video link")
		file       = flag.String("file", "", "local video or audio file")
		transcript = flag.String("transcript", "", "existing whisper.cpp JSON (-oj output)")
		videoURL   = flag.String("video-url", "", "link to show on the site, when using -file or -transcript")
		date       = flag.String("date", "", "date of the class, YYYY-MM-DD (default: upload date of -url)")
		title      = flag.String("title", "", "topic (default: title of -url)")
		course     = flag.Int64("course", 0, "course_id (see the site or the courses MCP tool)")
		event      = flag.Int64("event", 0, "id of the class in the calendar")
		server     = flag.String("server", os.Getenv("E2_URL"), "site address")
		key        = flag.String("key", os.Getenv("E2_KEY"), "key from the Telegram bot")
		model      = flag.String("model", envOr("WHISPER_MODEL", filepath.Join(home, ".cache/whisper-models/ggml-large-v3-turbo.bin")), "whisper.cpp model")
		lang       = flag.String("lang", "uk", "speech language")
		prompt     = flag.String("prompt", "", "text with the right spelling of names and terms; may make whisper repeat phrases, check the result")
		out        = flag.String("out", "", "write the request JSON here instead of uploading")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	l := lecture{Date: *date, Title: *title, VideoURL: *videoURL}
	if *course > 0 {
		l.CourseID = course
	}
	if *event > 0 {
		l.EventID = event
	}
	if err := run(&l, *link, *file, *transcript, *model, *lang, *prompt); err != nil {
		fail(err)
	}
	if l.Date == "" || l.Title == "" {
		fail(errors.New("вкажи -date і -title"))
	}

	body, err := json.Marshal(l)
	if err != nil {
		fail(err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, body, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("збережено %s: %d шматків, %s\n", *out, len(l.Transcript), clock(l.DurationS))
		return
	}
	if *server == "" || *key == "" {
		fail(errors.New("задай E2_URL і E2_KEY (або -server і -key), або -out FILE"))
	}
	id, err := upload(strings.TrimRight(*server, "/"), *key, body)
	if err != nil {
		fail(err)
	}
	fmt.Printf("додано лекцію #%d «%s» (%s, %d шматків транскрипту)\n", id, l.Title, clock(l.DurationS), len(l.Transcript))
}

func run(l *lecture, link, file, transcript, model, lang, prompt string) error {
	if transcript != "" {
		return readWhisper(l, transcript)
	}
	if (link == "") == (file == "") {
		return errors.New("потрібен рівно один з -url, -file або -transcript (див. -h)")
	}
	dir, err := os.MkdirTemp("", "lecture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	if link != "" {
		if l.VideoURL == "" {
			l.VideoURL = link
		}
		if file, err = download(l, link, dir); err != nil {
			return err
		}
	}
	wav := filepath.Join(dir, "audio.wav")
	step("звук → 16 кГц моно")
	if err := cmd("ffmpeg", "-loglevel", "error", "-y", "-i", file, "-vn", "-ar", "16000", "-ac", "1", wav); err != nil {
		return err
	}
	step("розпізнавання (whisper, може зайняти кілька хвилин)")
	start := time.Now()
	base := filepath.Join(dir, "transcript")
	args := []string{"-m", model, "-l", lang, "-f", wav, "-oj", "-of", base, "-np"}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	if err := cmd("whisper-cli", args...); err != nil {
		return err
	}
	step(fmt.Sprintf("готово за %s", time.Since(start).Round(time.Second)))
	return readWhisper(l, base+".json")
}

// download fetches the audio with yt-dlp and fills in the title and date it reports.
func download(l *lecture, link, dir string) (string, error) {
	ytdlp := envOr("YT_DLP", "yt-dlp")
	step("відомості про відео")
	info, err := exec.Command(ytdlp, "--no-playlist", "--skip-download", "--print", "%(title)s\n%(upload_date)s", link).Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp: %w (встанови: brew install yt-dlp)", err)
	}
	lines := strings.Split(strings.TrimSpace(string(info)), "\n")
	if l.Title == "" && len(lines) > 0 && lines[0] != "NA" {
		l.Title = lines[0]
	}
	if d := lines[len(lines)-1]; l.Date == "" && len(d) == 8 {
		l.Date = d[:4] + "-" + d[4:6] + "-" + d[6:]
		step("дата " + l.Date + " (дата завантаження відео; інша — через -date)")
	}
	step("завантаження звуку")
	if err := cmd(ytdlp, "--no-playlist", "-q", "--no-warnings", "-f", "bestaudio/best", "-o", filepath.Join(dir, "source.%(ext)s"), link); err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "source.*"))
	if len(matches) == 0 {
		return "", errors.New("yt-dlp нічого не завантажив")
	}
	return matches[0], nil
}

// fixes are misspellings whisper makes in Ukrainian academic speech. A
// --prompt with the right spelling helps less and can make whisper invent
// repeated phrases, so they are corrected afterwards instead.
var fixes = strings.NewReplacer(
	"диссертац", "дисертац", "Диссертац", "Дисертац",
	"доброчисн", "доброчесн", "Доброчисн", "Доброчесн",
)

// readWhisper converts whisper.cpp JSON output into segments in seconds.
func readWhisper(l *lecture, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var w struct {
		Transcription []struct {
			Offsets struct{ From, To int } `json:"offsets"`
			Text    string                 `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, t := range w.Transcription {
		text := fixes.Replace(strings.TrimSpace(t.Text))
		if text == "" {
			continue
		}
		l.Transcript = append(l.Transcript, segment{Start: t.Offsets.From / 1000, End: t.Offsets.To / 1000, Text: text})
	}
	if len(l.Transcript) == 0 {
		return errors.New("транскрипт порожній")
	}
	l.DurationS = l.Transcript[len(l.Transcript)-1].End
	return nil
}

func upload(server, key string, body []byte) (int64, error) {
	req, err := http.NewRequest(http.MethodPost, server+"/api/lectures", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	}
	json.Unmarshal(raw, &out)
	if res.StatusCode != http.StatusOK {
		if out.Error == "" {
			out.Error = strings.TrimSpace(string(raw))
		}
		return 0, fmt.Errorf("сервер відповів %d: %s", res.StatusCode, out.Error)
	}
	return out.ID, nil
}

func cmd(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func step(s string) { fmt.Fprintln(os.Stderr, "→ "+s) }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "помилка:", err)
	os.Exit(1)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func clock(sec int) string {
	return strconv.Itoa(sec/3600) + fmt.Sprintf(":%02d:%02d", sec/60%60, sec%60)
}
