// checkin.go 面板的「签到中心」端点：多站点签到的增删改查、手动触发、设置与历史。
//
// 端点都在 /panel/api/checkin/* 下，鉴权与其余 /panel/api/* 相同（withAuth）。
// 机密不出库：列表与回显只给 cookie_set / cookie_hint(末4位) / token_set，
// Cookie/Token 原文永远只在服务端。
package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/checkin"
)

// writeCheckinErr 统一错误响应：中文 + 4xx。
func writeCheckinErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

// decodeCheckinBody 解析 JSON body。空 body 合法（全部字段按缺省处理）；
// 其余解析失败统一回「请求体不是合法 JSON」。
func decodeCheckinBody(r *http.Request) (checkin.Input, error) {
	var in checkin.Input
	if r.Body == nil {
		return in, nil
	}
	err := json.NewDecoder(r.Body).Decode(&in)
	if err == nil || errors.Is(err, io.EOF) {
		return in, nil
	}
	return in, errors.New("请求体不是合法 JSON")
}

// checkinSnapshot GET /panel/api/checkin：全量快照（设置 + 站点 + 汇总）。
func (p *Panel) checkinSnapshot(w http.ResponseWriter, r *http.Request) {
	snap := p.Checkin.Snapshot()
	writeJSON(w, http.StatusOK, snap)
}

// checkinSiteCreate POST /panel/api/checkin/sites
func (p *Panel) checkinSiteCreate(w http.ResponseWriter, r *http.Request) {
	in, err := decodeCheckinBody(r)
	if err != nil {
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	view, err := p.Checkin.Create(in)
	if err != nil {
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": view})
}

// checkinSiteUpdate POST /panel/api/checkin/sites/{id}
func (p *Panel) checkinSiteUpdate(w http.ResponseWriter, r *http.Request) {
	in, err := decodeCheckinBody(r)
	if err != nil {
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	view, err := p.Checkin.Update(r.PathValue("id"), in)
	if err != nil {
		if errors.Is(err, checkin.ErrNotFound) {
			writeCheckinErr(w, http.StatusNotFound, err)
			return
		}
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": view})
}

// checkinSiteDelete POST /panel/api/checkin/sites/{id}/delete
func (p *Panel) checkinSiteDelete(w http.ResponseWriter, r *http.Request) {
	if err := p.Checkin.Delete(r.PathValue("id")); err != nil {
		if errors.Is(err, checkin.ErrNotFound) {
			writeCheckinErr(w, http.StatusNotFound, err)
			return
		}
		writeCheckinErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// checkinSiteRun POST /panel/api/checkin/sites/{id}/run：立即签单个站点。
func (p *Panel) checkinSiteRun(w http.ResponseWriter, r *http.Request) {
	res, err := p.Checkin.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, checkin.ErrNotFound) {
			writeCheckinErr(w, http.StatusNotFound, err)
			return
		}
		writeCheckinErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// checkinRunAll POST /panel/api/checkin/run_all：一键全签。
// 已有一轮在跑 → 409（前端把按钮置忙碌态，用户不会等到这个响应，属于兜底）。
func (p *Panel) checkinRunAll(w http.ResponseWriter, r *http.Request) {
	results, summary, err := p.Checkin.RunAll(r.Context())
	if err != nil {
		if errors.Is(err, checkin.ErrBusy) {
			writeCheckinErr(w, http.StatusConflict, err)
			return
		}
		writeCheckinErr(w, http.StatusInternalServerError, err)
		return
	}
	if results == nil {
		results = []checkin.SiteResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "summary": summary})
}

// checkinSettings POST /panel/api/checkin/settings
func (p *Panel) checkinSettings(w http.ResponseWriter, r *http.Request) {
	var st checkin.Settings
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			writeCheckinErr(w, http.StatusBadRequest, errors.New("请求体不是合法 JSON"))
			return
		}
	}
	saved, err := p.Checkin.SetSettings(st)
	if err != nil {
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": saved})
}

// checkinTest POST /panel/api/checkin/test：试运行表单配置（不落库）。
func (p *Panel) checkinTest(w http.ResponseWriter, r *http.Request) {
	in, err := decodeCheckinBody(r)
	if err != nil {
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := p.Checkin.Test(r.Context(), in)
	if err != nil {
		writeCheckinErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// checkinHistory GET /panel/api/checkin/history?site=<id>&limit=50
func (p *Panel) checkinHistory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("site"))
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	items := p.Checkin.History(id, limit)
	if items == nil {
		items = []checkin.Result{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// checkinGuard 面板未接线（Checkin == nil）时的兜底：501 与其他可选端点同风格。
func (p *Panel) checkinGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p.Checkin == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "签到中心未启用"})
			return
		}
		next(w, r)
	}
}
