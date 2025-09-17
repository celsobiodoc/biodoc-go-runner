package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"mime"
)

/* ==================== Config & Helpers ==================== */

var httpClient = &http.Client{Timeout: 20 * time.Second}
var quiet bool // controlado por --quiet/-q

// remove --quiet/-q de qualquer posição e retorna args limpos + se quiet foi pedido
func stripQuiet(all []string) ([]string, bool) {
	out := make([]string, 0, len(all))
	q := false
	for _, a := range all {
		if a == "--quiet" || a == "-q" {
			q = true
			continue
		}
		out = append(out, a)
	}
	return out, q
}

// pega valor do ambiente com default
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ID padrão (pode sobrescrever via .env: CARD_ID=...)
const hardDefaultID = "99980000999999993"

func defaultID() string {
	if v := os.Getenv("CARD_ID"); v != "" {
		return v
	}
	return hardDefaultID
}

// guess MIME from file extension (fallback jpeg)
func guessMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "image/jpeg"
	}
	typ := mime.TypeByExtension(ext)
	if typ == "" {
		if ext == ".jpg" || ext == ".jpeg" {
			return "image/jpeg"
		}
		if ext == ".png" {
			return "image/png"
		}
		if ext == ".webp" {
			return "image/webp"
		}
		return "image/jpeg"
	}
	if i := strings.IndexByte(typ, ';'); i > 0 {
		typ = typ[:i]
	}
	return typ
}

func buildDataURIImage(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := guessMIME(path)
	b64 := base64.StdEncoding.EncodeToString(b)
	return "data:" + m + ";base64," + b64, nil
}

func readImageAsBase64(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func authHeader(token string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", "Bearer "+token)
	h.Set("Content-Type", "application/json")
	return h
}

func doJSON(method, url string, headers http.Header, body any) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		jb, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(jb)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	for k, vv := range headers {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("read body: %w", err)
	}
	return resp, b, nil
}

/* ==================== Tipos de resposta ==================== */

type VerifyResponse struct {
	Percentage string `json:"percentage"`
	Response   struct {
		IDLog       string `json:"id_Log"`
		Percentage  string `json:"percentage"`
		Success     bool   `json:"success"`
		Status      int    `json:"status"`
		Message     string `json:"message"`
		ReferenceID string `json:"reference_Id"`
	} `json:"response"`
}

/* ==================== Comandos ==================== */

// POST /api/card/integration/register
func cmdCreateCard(baseURL, token, imagePath, id, name string, consent bool) error {
	img64, err := readImageAsBase64(imagePath)
	if err != nil {
		return fmt.Errorf("ler imagem: %w", err)
	}
	payload := map[string]any{
		"id":                 id,
		"name":               name,
		"consentTermSigned":  consent,
		"image":              img64,
	}
	url := strings.TrimRight(baseURL, "/") + "/api/card/integration/register"
	resp, body, err := doJSON(http.MethodPost, url, authHeader(token), payload)
	if err != nil {
		return err
	}
	fmt.Printf("status=%d\n", resp.StatusCode)
	if !quiet {
		fmt.Println(string(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("requisição falhou: %d", resp.StatusCode)
	}
	return nil
}

// GET /api/card/integration/mainimage (header idCard); salva arquivo
func cmdMainImage(baseURL, token, idCard, outPath string) error {
	url := strings.TrimRight(baseURL, "/") + "/api/card/integration/mainimage"
	h := authHeader(token)
	h.Set("idCard", idCard)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, vv := range h {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("status=%d\n", resp.StatusCode)
	if resp.StatusCode != 200 {
		if !quiet {
			fmt.Println(string(b))
		}
		return fmt.Errorf("esperado 200, veio %d", resp.StatusCode)
	}
	if outPath == "" {
		outPath = "mainimage.bin"
	}
	if err := os.WriteFile(outPath, b, 0644); err != nil {
		return err
	}
	fmt.Printf("imagem salva em %s (%d bytes)\n", outPath, len(b))
	return nil
}

// POST /api/card/integration/verify (JSON com data-uri)
func cmdVerifyCard(baseURL, token, endpointPath, imagePath, id, name, detail string) error {
	if endpointPath == "" {
		endpointPath = "/api/card/integration/verify"
	}
	dataURI, err := buildDataURIImage(imagePath)
	if err != nil {
		return fmt.Errorf("ler/encode imagem: %w", err)
	}
	body := map[string]any{
		"id":     id,
		"name":   name,
		"detail": detail,
		"image":  dataURI,
	}

	url := strings.TrimRight(baseURL, "/") + endpointPath
	h := authHeader(token)

	fmt.Printf("[verify] POST %s (JSON)\n", url)
	resp, raw, err := doJSON(http.MethodPost, url, h, body)
	if err != nil {
		return err
	}
	fmt.Printf("status=%d\n", resp.StatusCode)
	if !quiet {
		fmt.Println(string(raw))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("requisição falhou: %d", resp.StatusCode)
	}

	var vresp VerifyResponse
	if err := json.Unmarshal(raw, &vresp); err == nil {
		ok := "❌"
		if vresp.Response.Success {
			ok = "✅"
		}
		pct := vresp.Response.Percentage
		if pct == "" {
			pct = vresp.Percentage
		}
		fmt.Printf("[verify] %s match | similaridade=%s | status=%d | idLog=%s\n",
			ok, pct, vresp.Response.Status, vresp.Response.IDLog)
	}
	return nil
}

// DELETE /api/card/{id}
func cmdDeleteCard(baseURL, token, id string) error {
	if id == "" {
		return fmt.Errorf("--id vazio (defina CARD_ID no .env ou use defaultID())")
	}
	url := strings.TrimRight(baseURL, "/") + "/api/card/" + id

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	for k, vv := range authHeader(token) {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("status=%d\n", resp.StatusCode)
	if len(body) > 0 && !quiet {
		fmt.Println(string(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("falha ao deletar: %d", resp.StatusCode)
	}
	return nil
}

// Deleta ignorando 404/422 (registro não existe)
func cmdDeleteCardIgnore404(baseURL, token, id string) error {
	err := cmdDeleteCard(baseURL, token, id)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "422") {
			fmt.Printf("[preclean] id=%s não existe ou já foi deletado, seguindo…\n", id)
			return nil
		}
		return err
	}
	fmt.Printf("[preclean] id=%s deletado\n", id)
	return nil
}

/* ==================== UI ==================== */

func usage() {
	fmt.Println("biodoc-go-runner")
	fmt.Println()
	fmt.Println("Comandos:")
	fmt.Println("  create-card   - Cria card a partir de imagem")
	fmt.Println("  verify-card   - Verifica imagem atual (POST /api/card/integration/verify)")
	fmt.Println("  delete-card   - Deleta a carteirinha (DELETE /api/card/{id})")
	fmt.Println("  main-image    - Baixa imagem principal (header idCard)")
	fmt.Println("  run-all       - preclean → create → verify → delete")
	fmt.Println()
	fmt.Println("Geral (ENV): BASE_URL, AUTH_TOKEN, CARD_ID (opcional)")
}

/* ==================== main ==================== */

func main() {
	// .env
	if err := godotenv.Load(); err != nil {
		fmt.Println("Erro ao carregar o arquivo .env")
	}

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	// aceita --quiet/-q em qualquer posição
	args, q := stripQuiet(os.Args[1:])
	quiet = q

	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	cmd := args[0]

	baseURL := envOr("BASE_URL", "https://api.develop.biodoc.com.br")
	token := os.Getenv("AUTH_TOKEN")
	if token == "" {
		fmt.Println("[aviso] AUTH_TOKEN não definido; endpoints protegidos vão falhar")
	}

	switch cmd {

	case "create-card":
		fs := flag.NewFlagSet("create-card", flag.ExitOnError)
		imagePath := fs.String("image", `image\created_1.jpg`, "caminho da imagem")
		id := fs.String("id", defaultID(), "documento/id do card")
		name := fs.String("name", "Celso QA", "nome")
		consent := fs.Bool("consent", false, "consentTermSigned")
		_ = fs.Parse(args[1:])
		if err := cmdCreateCard(baseURL, token, *imagePath, *id, *name, *consent); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "main-image":
		fs := flag.NewFlagSet("main-image", flag.ExitOnError)
		idCard := fs.String("idcard", "", "valor do header idCard (obrigatório)")
		out := fs.String("out", "", "arquivo de saída (default: mainimage.bin)")
		_ = fs.Parse(args[1:])
		if *idCard == "" {
			fmt.Fprintln(os.Stderr, "--idcard é obrigatório")
			os.Exit(2)
		}
		if err := cmdMainImage(baseURL, token, *idCard, *out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "verify-card":
		fs := flag.NewFlagSet("verify-card", flag.ExitOnError)
		endpoint := fs.String("endpoint", "/api/card/integration/verify", "path da rota verify")
		imagePath := fs.String("image", `image\created_1.jpg`, "imagem para verificação")
		id := fs.String("id", defaultID(), "id do cadastro (string)")
		name := fs.String("name", "Celso QA", "nome")
		detail := fs.String("detail", "", "detalhes (string). Ex.: \"{'guia': '654321', ...}\"")
		_ = fs.Parse(args[1:])
		if err := cmdVerifyCard(baseURL, token, *endpoint, *imagePath, *id, *name, *detail); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "delete-card":
		fs := flag.NewFlagSet("delete-card", flag.ExitOnError)
		id := fs.String("id", defaultID(), "ID do card para deletar (usa CARD_ID ou default se vazio)")
		_ = fs.Parse(args[1:])
		if err := cmdDeleteCard(baseURL, token, *id); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "run-all":
		fs := flag.NewFlagSet("run-all", flag.ExitOnError)
		image := fs.String("image", `image\created_1.jpg`, "imagem para criar/verificar")
		id := fs.String("id", defaultID(), "id do card (usa CARD_ID do .env se existir)")
		name := fs.String("name", "Celso QA", "nome")
		detail := fs.String("detail", "{'guia':'654321'}", "detail (string)")
		preclean := fs.Bool("preclean", true, "deletar antes se existir (ignora 404/422)")
		_ = fs.Parse(args[1:])

		if *preclean {
			if err := cmdDeleteCardIgnore404(baseURL, token, *id); err != nil {
				fmt.Println("preclean falhou:", err)
				os.Exit(1)
			}
		}
		if err := cmdCreateCard(baseURL, token, *image, *id, *name, true); err != nil {
			fmt.Println("create falhou:", err)
			os.Exit(1)
		}
		if err := cmdVerifyCard(baseURL, token, "/api/card/integration/verify", *image, *id, *name, *detail); err != nil {
			fmt.Println("verify falhou:", err)
			os.Exit(1)
		}
		if err := cmdDeleteCard(baseURL, token, *id); err != nil {
			fmt.Println("delete final falhou:", err)
			os.Exit(1)
		}
		fmt.Println("✅ fluxo completo: preclean → create → verify → delete")

	default:
		usage()
		fmt.Println()
		fmt.Println("Exemplos:")
		fmt.Println("  go run . create-card --image imagens/criacao.jpg --id 123 --name 'Fulano' --consent=true")
		fmt.Println("  go run . verify-card --image imagens/selfie.jpg --id 123")
		fmt.Println("  go run . delete-card --id 123")
		fmt.Println("  go run . run-all")
	}

	_ = filepath.Base("") // evita warning de import
}
