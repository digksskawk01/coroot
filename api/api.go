package api

import (
	"context"
	"errors"
	"github.com/coroot/coroot/alerts"
	"github.com/coroot/coroot/api/views"
	"github.com/coroot/coroot/cache"
	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/prom"
	"github.com/coroot/coroot/stats"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/gorilla/mux"
	"k8s.io/klog"
	"net/http"
	"sort"
	"time"
)

type Api struct {
	cache    *cache.Cache
	db       *db.DB
	stats    *stats.Collector
	readOnly bool
}

func NewApi(cache *cache.Cache, db *db.DB, stats *stats.Collector, readOnly bool) *Api {
	return &Api{cache: cache, db: db, stats: stats, readOnly: readOnly}
}

func (api *Api) Projects(w http.ResponseWriter, r *http.Request) {
	api.stats.RegisterRequest(r)
	projects, err := api.db.GetProjectNames()
	if err != nil {
		klog.Errorln("failed to get projects:", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	type Project struct {
		Id   db.ProjectId `json:"id"`
		Name string       `json:"name"`
	}
	res := make([]Project, 0, len(projects))
	for id, name := range projects {
		res = append(res, Project{Id: id, Name: name})
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Name < res[j].Name
	})
	utils.WriteJson(w, res)
}

func (api *Api) Project(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := db.ProjectId(vars["project"])

	switch r.Method {

	case http.MethodGet:
		res := ProjectForm{}
		res.Prometheus.RefreshInterval = db.DefaultRefreshInterval
		if id != "" {
			project, err := api.db.GetProject(id)
			if err != nil {
				if errors.Is(err, db.ErrNotFound) {
					klog.Warningln("project not found:", id)
					return
				}
				klog.Errorln("failed to get project:", err)
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
			res.Name = project.Name
			res.Prometheus = project.Prometheus
			if api.readOnly {
				res.Prometheus.Url = "http://<hidden>"
			}
		}
		utils.WriteJson(w, res)

	case http.MethodPost:
		if api.readOnly {
			return
		}
		var form ProjectForm
		if err := ReadAndValidate(r, &form); err != nil {
			klog.Warningln("bad request:", err)
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		project := db.Project{
			Id:         id,
			Name:       form.Name,
			Prometheus: form.Prometheus,
		}
		p := project.Prometheus
		user, password := "", ""
		if p.BasicAuth != nil {
			user, password = p.BasicAuth.User, p.BasicAuth.Password
		}
		promClient, err := prom.NewApiClient(p.Url, user, password, p.TlsSkipVerify)
		if err != nil {
			klog.Errorln("failed to get api client:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := promClient.Ping(ctx); err != nil {
			klog.Warningln("failed to ping prometheus:", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		id, err := api.db.SaveProject(project)
		if err != nil {
			if errors.Is(err, db.ErrConflict) {
				http.Error(w, "This project name is already being used.", http.StatusConflict)
				return
			}
			klog.Errorln("failed to save project:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		http.Error(w, string(id), http.StatusOK)

	case http.MethodDelete:
		if api.readOnly {
			return
		}
		if err := api.db.DeleteProject(id); err != nil {
			klog.Errorln("failed to delete project:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		http.Error(w, "", http.StatusOK)

	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func (api *Api) Status(w http.ResponseWriter, r *http.Request) {
	projectId := db.ProjectId(mux.Vars(r)["project"])
	if r.Method == http.MethodPost {
		if api.readOnly {
			return
		}
		var form ProjectStatusForm
		if err := ReadAndValidate(r, &form); err != nil {
			klog.Warningln("bad request:", err)
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		var appType model.ApplicationType
		var mute bool
		switch {
		case form.Mute != nil:
			mute = true
			appType = *form.Mute
		case form.UnMute != nil:
			mute = false
			appType = *form.UnMute
		}
		if err := api.db.ToggleConfigurationHint(projectId, appType, mute); err != nil {
			klog.Errorln("failed to toggle:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		return
	}
	project, err := api.db.GetProject(projectId)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			klog.Warningln("project not found:", projectId)
			utils.WriteJson(w, views.Status(nil, nil, nil))
			return
		}
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	cacheStatus, err := api.cache.GetCacheClient(project).GetStatus()
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	now := timeseries.Now()
	world, err := api.loadWorld(r.Context(), project, now.Add(-timeseries.Hour), now)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	utils.WriteJson(w, views.Status(project, cacheStatus, world))
}

func (api *Api) Overview(w http.ResponseWriter, r *http.Request) {
	world, project, err := api.loadWorldByRequest(r)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if world == nil {
		return
	}
	utils.WriteJson(w, views.Overview(world, project))
}

func (api *Api) Search(w http.ResponseWriter, r *http.Request) {
	world, _, err := api.loadWorldByRequest(r)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if world == nil {
		return
	}
	utils.WriteJson(w, views.Search(world))
}

func (api *Api) Configs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectId := db.ProjectId(vars["project"])
	checkConfigs, err := api.db.GetCheckConfigs(projectId)
	if err != nil {
		klog.Errorln("failed to get check configs:", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	utils.WriteJson(w, views.Configs(checkConfigs))
}

func (api *Api) Categories(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectId := db.ProjectId(vars["project"])

	if r.Method == http.MethodPost {
		if api.readOnly {
			return
		}
		var form ApplicationCategoryForm
		if err := ReadAndValidate(r, &form); err != nil {
			klog.Warningln("bad request:", err)
			http.Error(w, "Invalid name or patterns", http.StatusBadRequest)
			return
		}
		if err := api.db.SaveApplicationCategory(projectId, form.Name, form.NewName, form.customPatterns); err != nil {
			klog.Errorln("failed to save:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		return
	}

	p, err := api.db.GetProject(projectId)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	utils.WriteJson(w, views.Categories(p))
}

func (api *Api) Integrations(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectId := db.ProjectId(vars["project"])

	if r.Method == http.MethodPost {
		if api.readOnly {
			return
		}
		var form IntegrationsForm
		if err := ReadAndValidate(r, &form); err != nil {
			klog.Warningln("bad request:", err)
			http.Error(w, "Invalid base url", http.StatusBadRequest)
			return
		}
		if err := api.db.SaveIntegrationsBaseUrl(projectId, form.BaseUrl); err != nil {
			klog.Errorln("failed to save:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		return
	}

	p, err := api.db.GetProject(projectId)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	utils.WriteJson(w, views.Integrations(r.Context(), p))
}

func (api *Api) IntegrationsSlack(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectId := db.ProjectId(vars["project"])

	var form IntegrationsSlackForm

	if r.Method == http.MethodPost {
		if api.readOnly {
			return
		}
		if err := ReadAndValidate(r, &form); err != nil {
			klog.Warningln("bad request:", err)
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		ok, err := alerts.NewSlack(form.Token).IsChannelAvailable(r.Context(), form.Channel)
		if err != nil {
			http.Error(w, "Invalid token", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "Channel is not available", http.StatusBadRequest)
			return
		}
		if err := api.db.SaveIntegrationsSlack(projectId, &db.IntegrationSlack{
			Token:          form.Token,
			DefaultChannel: form.Channel,
			Enabled:        form.Enabled,
		}); err != nil {
			klog.Errorln("failed to save:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		return
	}

	if r.Method == http.MethodDelete {
		if api.readOnly {
			return
		}
		if err := api.db.SaveIntegrationsSlack(projectId, nil); err != nil {
			klog.Errorln("failed to delete:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		return
	}

	p, err := api.db.GetProject(projectId)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if cfg := p.Settings.Integrations.Slack; cfg != nil {
		form.Token = cfg.Token
		if api.readOnly {
			form.Token = "<token>"
		}
		form.Channel = cfg.DefaultChannel
		form.Enabled = cfg.Enabled
	} else {
		form.Enabled = true
	}
	utils.WriteJson(w, form)
}

func (api *Api) Prom(w http.ResponseWriter, r *http.Request) {
	projectId := db.ProjectId(mux.Vars(r)["project"])
	project, err := api.db.GetProject(projectId)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	p := project.Prometheus
	user, password := "", ""
	if p.BasicAuth != nil {
		user, password = p.BasicAuth.User, p.BasicAuth.Password
	}
	c, err := prom.NewApiClient(p.Url, user, password, p.TlsSkipVerify)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	c.Proxy(r, w)
}

func (api *Api) App(w http.ResponseWriter, r *http.Request) {
	id, err := model.NewApplicationIdFromString(mux.Vars(r)["app"])
	if err != nil {
		klog.Warningf("invalid application_id %s: %s ", mux.Vars(r)["app"], err)
		http.Error(w, "invalid application_id: "+mux.Vars(r)["app"], http.StatusBadRequest)
		return
	}
	world, project, err := api.loadWorldByRequest(r)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if world == nil {
		return
	}
	app := world.GetApplication(id)
	if app == nil {
		klog.Warningln("application not found:", id)
		http.Error(w, "Application not found", http.StatusNotFound)
		return
	}
	incidents, err := api.db.GetIncidentsByApp(project.Id, app.Id, world.Ctx.From, world.Ctx.To)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	utils.WriteJson(w, views.Application(world, app, incidents))
}

func (api *Api) Check(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectId := db.ProjectId(vars["project"])
	appId, err := model.NewApplicationIdFromString(vars["app"])
	if err != nil {
		klog.Warningf("invalid application_id %s: %s ", vars["app"], err)
		http.Error(w, "invalid application_id: "+vars["app"], http.StatusBadRequest)
		return
	}
	checkId := model.CheckId(vars["check"])

	switch r.Method {

	case http.MethodGet:
		project, err := api.db.GetProject(projectId)
		if err != nil {
			klog.Errorln("failed to get project:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		checkConfigs, err := api.db.GetCheckConfigs(projectId)
		if err != nil {
			klog.Errorln("failed to get check configs:", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		res := struct {
			Form         any               `json:"form"`
			Integrations map[string]string `json:"integrations"`
		}{
			Integrations: map[string]string{},
		}
		if cfg := project.Settings.Integrations.Slack; cfg != nil && cfg.Enabled {
			res.Integrations["slack"] = cfg.DefaultChannel
		}
		switch checkId {
		case model.Checks.SLOAvailability.Id:
			form := CheckConfigSLOAvailabilityForm{
				Configs: checkConfigs.GetAvailability(appId),
			}
			if len(form.Configs) == 0 {
				form.Configs = append(form.Configs, model.CheckConfigSLOAvailability{
					TotalRequestsQuery:  "",
					FailedRequestsQuery: "",
					ObjectivePercentage: model.Checks.SLOAvailability.DefaultThreshold,
				})
				form.Empty = true
			}
			res.Form = form
		case model.Checks.SLOLatency.Id:
			form := CheckConfigSLOLatencyForm{
				Configs: checkConfigs.GetLatency(appId),
			}
			if len(form.Configs) == 0 {
				form.Configs = append(form.Configs, model.CheckConfigSLOLatency{
					HistogramQuery:      "",
					ObjectiveBucket:     0.1,
					ObjectivePercentage: model.Checks.SLOLatency.DefaultThreshold,
				})
				form.Empty = true
			}
			res.Form = form
		default:
			form := CheckConfigForm{
				Configs: checkConfigs.GetSimpleAll(checkId, appId),
			}
			if len(form.Configs) == 0 {
				http.Error(w, "", http.StatusNotFound)
				return
			}
			res.Form = form
		}
		utils.WriteJson(w, res)
		return

	case http.MethodPost:
		if api.readOnly {
			return
		}
		switch checkId {
		case model.Checks.SLOAvailability.Id:
			var form CheckConfigSLOAvailabilityForm
			if err := ReadAndValidate(r, &form); err != nil {
				klog.Warningln("bad request:", err)
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			if err := api.db.SaveCheckConfig(projectId, appId, checkId, form.Configs); err != nil {
				klog.Errorln("failed to save check config:", err)
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
		case model.Checks.SLOLatency.Id:
			var form CheckConfigSLOLatencyForm
			if err := ReadAndValidate(r, &form); err != nil {
				klog.Warningln("bad request:", err)
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			if err := api.db.SaveCheckConfig(projectId, appId, checkId, form.Configs); err != nil {
				klog.Errorln("failed to save check config:", err)
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
		default:
			var form CheckConfigForm
			if err := ReadAndValidate(r, &form); err != nil {
				klog.Warningln("bad request:", err)
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			for level, cfg := range form.Configs {
				var id model.ApplicationId
				switch level {
				case 0:
					continue
				case 1:
					id = model.ApplicationIdZero
				case 2:
					id = appId
				}
				if err := api.db.SaveCheckConfig(projectId, id, checkId, cfg); err != nil {
					klog.Errorln("failed to save check config:", err)
					http.Error(w, "", http.StatusInternalServerError)
					return
				}
			}
			return
		}
	}
}

func (api *Api) Node(w http.ResponseWriter, r *http.Request) {
	nodeName := mux.Vars(r)["node"]
	world, _, err := api.loadWorldByRequest(r)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if world == nil {
		return
	}
	node := world.GetNode(nodeName)
	if node == nil {
		klog.Warningf("node not found: %s ", nodeName)
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}
	utils.WriteJson(w, views.Node(world, node))
}

func (api *Api) loadWorld(ctx context.Context, project *db.Project, from, to timeseries.Time) (*model.World, error) {
	cc := api.cache.GetCacheClient(project)
	cacheTo, err := cc.GetTo()
	if err != nil {
		return nil, err
	}

	step := project.Prometheus.RefreshInterval
	from = from.Truncate(step)
	to = to.Truncate(step)

	if cacheTo.IsZero() || cacheTo.Before(from) {
		return nil, nil
	}

	duration := to.Sub(from)
	if cacheTo.Before(to) {
		to = cacheTo
		from = to.Add(-duration)
	}
	step = increaseStepForBigDurations(duration, step)

	checkConfigs, err := api.db.GetCheckConfigs(project.Id)
	if err != nil {
		return nil, err
	}

	world, err := constructor.New(cc, project.Prometheus.RefreshInterval, checkConfigs).LoadWorld(ctx, from, to, step, nil)
	return world, err
}

func (api *Api) loadWorldByRequest(r *http.Request) (*model.World, *db.Project, error) {
	projectId := db.ProjectId(mux.Vars(r)["project"])
	project, err := api.db.GetProject(projectId)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			klog.Warningln("project not found:", projectId)
			return nil, nil, nil
		}
		return nil, nil, err
	}

	now := timeseries.Now()
	q := r.URL.Query()
	from := utils.ParseTimeFromUrl(now, q, "from", now.Add(-timeseries.Hour))
	to := utils.ParseTimeFromUrl(now, q, "to", now)

	incidentKey := q.Get("incident")
	if incidentKey != "" {
		if incident, err := api.db.GetIncidentByKey(projectId, incidentKey); err != nil {
			klog.Warningln("failed to get incident:", err)
		} else {
			from = incident.OpenedAt.Add(-timeseries.Hour)
			if !incident.ResolvedAt.IsZero() && incident.ResolvedAt.Add(timeseries.Hour).Before(to) {
				to = incident.ResolvedAt.Add(timeseries.Hour)
			}
		}
	}

	world, err := api.loadWorld(r.Context(), project, from, to)
	return world, project, err
}

func increaseStepForBigDurations(duration, step timeseries.Duration) timeseries.Duration {
	switch {
	case duration > 5*24*timeseries.Hour:
		return maxDuration(step, 60*timeseries.Minute)
	case duration > 24*timeseries.Hour:
		return maxDuration(step, 15*timeseries.Minute)
	case duration > 12*timeseries.Hour:
		return maxDuration(step, 10*timeseries.Minute)
	case duration > 6*timeseries.Hour:
		return maxDuration(step, 5*timeseries.Minute)
	case duration > 4*timeseries.Hour:
		return maxDuration(step, timeseries.Minute)
	}
	return step
}

func maxDuration(d1, d2 timeseries.Duration) timeseries.Duration {
	if d1 >= d2 {
		return d1
	}
	return d2
}
