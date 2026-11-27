package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"sipvoice/internal/config"
	"sipvoice/internal/configbundle"
	"sipvoice/internal/fax"
)

// configDir 是配置文件所在目录。
const configDir = "/etc/sipvoice"

// handleConfigBundle 提供整机配置的导出与导入。
//
//	GET  /api/config/export?secrets=1   下载配置包
//	POST /api/config/import?dry_run=1   干跑校验
//	POST /api/config/import             实际导入
func (s *Server) handleConfigBundle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		includeSecrets := r.URL.Query().Get("secrets") == "1"
		b, err := configbundle.Export(configbundle.ExportOptions{
			Dir:            configDir,
			AppVersion:     s.opt.Version,
			IncludeSecrets: includeSecrets,
		})
		if err != nil {
			http.Error(w, "导出失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		raw, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := fmt.Sprintf("sipvoice-config-%s.json", time.Now().Format("20060102-150405"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
		log.Printf("[webui] 配置导出（含密钥=%v，%d 字节）", includeSecrets, len(raw))

	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, "读取请求体失败: "+err.Error(), 400)
			return
		}
		b, err := configbundle.Parse(body)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		dryRun := r.URL.Query().Get("dry_run") == "1"
		rep, err := configbundle.Import(b, configbundle.ImportOptions{Dir: configDir, DryRun: dryRun})
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
			return
		}

		// 导入后需要把变更推到运行时：重载全局配置、重渲染话机与传真拨号方案
		notes := []string{}
		if !dryRun {
			if c, err := config.Load(config.Path()); err == nil {
				config.Set(c)
			} else {
				notes = append(notes, "重载全局配置失败: "+err.Error())
			}
			// 话机台账变了就重渲染 pjsip/extensions
			if hasSection(rep, configbundle.SectionPhones) && s.opt.Phones != nil {
				if err := s.applyPhoneConfig(); err != nil {
					notes = append(notes, "应用话机配置失败: "+err.Error())
				} else {
					notes = append(notes, "话机配置已重新应用")
				}
			}
			// 传真配置变了就重渲染 fax 拨号方案（applyPhoneConfig 已包含，避免重复）
			if hasSection(rep, configbundle.SectionFax) && s.opt.Phones == nil {
				if err := fax.ApplyDialplan(fax.Load()); err != nil {
					notes = append(notes, "应用传真拨号方案失败: "+err.Error())
				} else {
					notes = append(notes, "传真拨号方案已重新生成")
				}
			}
		}
		if len(notes) > 0 {
			rep.Warnings = append(rep.Warnings, notes...)
		}

		msg := "配置已导入"
		if dryRun {
			msg = "校验通过（未写入）"
		}
		if !rep.OK {
			msg = "部分区段导入失败，详见报告"
		}
		log.Printf("[webui] 配置导入 dry_run=%v ok=%v 区段=%d 未知=%d 备份=%s",
			dryRun, rep.OK, len(rep.Sections), len(rep.Unknown), rep.BackupDir)

		writeJSON(w, map[string]any{"ok": rep.OK, "message": msg, "report": rep})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

func hasSection(rep *configbundle.Report, name string) bool {
	for _, s := range rep.Sections {
		if s.Name == name && s.Status == "applied" {
			return true
		}
	}
	return false
}

// summaryLines 把报告压成几行便于日志与面板展示。
func summaryLines(rep *configbundle.Report) string {
	var b strings.Builder
	for _, s := range rep.Sections {
		fmt.Fprintf(&b, "%s:%s ", s.Name, s.Status)
	}
	return strings.TrimSpace(b.String())
}
