// Recurring send-schedule popover wiring and Scheduled folder plan panel.
(function () {
  "use strict"

  var WEEKDAY_LABEL = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
  var populatedPanels = new WeakSet()

  function timezones() {
    try {
      if (window.Intl && typeof Intl.supportedValuesOf === "function") {
		return Intl.supportedValuesOf("timeZone")
      }
    } catch (_) {}
    return ["UTC", "America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
      "America/Sao_Paulo", "Europe/London", "Europe/Paris", "Europe/Berlin", "Africa/Cairo",
      "Asia/Kolkata", "Asia/Dubai", "Asia/Singapore", "Asia/Shanghai", "Asia/Tokyo",
      "Australia/Sydney", "Pacific/Auckland"]
  }

  function getCurrentTimezone() {
    if (typeof window.getGoferTimezone === "function") return window.getGoferTimezone()
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
  }

  // Populate one schedule popover (there is one reused compose popover).
  function initPopover(panel) {
    if (populatedPanels.has(panel)) return
    populatedPanels.add(panel)

    var tz = panel.querySelector("[data-compose-schedule-tz]")
    if (tz && !tz.options.length) {
      var current = getCurrentTimezone()
      timezones().forEach(function (zone) {
        var option = document.createElement("option")
        option.value = zone
        option.textContent = zone.replace(/_/g, " ")
        option.selected = zone === current
        tz.appendChild(option)
      })
      if (!tz.value) tz.value = current
      syncTimezoneLabel(panel, tz.value)
      tz.addEventListener("change", function () { syncTimezoneLabel(panel, tz.value) })
    }

    var repeat = panel.querySelector("[data-compose-schedule-repeat]")
    var weekdays = panel.querySelector("[data-compose-schedule-weekdays]")
    var hidden = panel.querySelector("[data-compose-schedule-weekdays-input]")
    if (repeat && weekdays) {
      repeat.addEventListener("change", function () {
        weekdays.hidden = repeat.value !== "weekly"
      })
    }
    panel.querySelectorAll("[data-compose-weekday]").forEach(function (button) {
      button.addEventListener("click", function () {
        var active = button.getAttribute("data-active") === "true"
        button.setAttribute("data-active", active ? "false" : "true")
        button.setAttribute("aria-pressed", active ? "false" : "true")
        syncWeekdays(panel)
      })
    })

    var calendarInput = panel.querySelector("[data-tui-calendar-hidden-input]")
    var endInput = panel.querySelector("[data-compose-schedule-end]")
    if (calendarInput && endInput) {
      calendarInput.addEventListener("change", function () {
        syncWeekdayFromStart(panel, hidden)
      })
    }
  }

  function syncTimezoneLabel(panel, value) {
    var label = panel.querySelector("[data-compose-schedule-timezone]")
    if (label) label.textContent = value
  }

  function syncWeekdays(panel) {
    var hidden = panel.querySelector("[data-compose-schedule-weekdays-input]")
    var days = []
    panel.querySelectorAll("[data-compose-weekday]").forEach(function (button) {
      if (button.getAttribute("data-active") === "true") days.push(button.getAttribute("data-compose-weekday"))
    })
    if (hidden) hidden.value = days.join(",")
  }

  function syncWeekdayFromStart(panel, hidden) {
    var repeat = panel.querySelector("[data-compose-schedule-repeat]")
    if (!repeat || repeat.value !== "weekly" || (hidden && hidden.value)) return
    var calendarInput = panel.querySelector("[data-tui-calendar-hidden-input]")
    var tz = panel.querySelector("[data-compose-schedule-tz]")
    if (!calendarInput || !calendarInput.value || !tz) return
    var parts = calendarInput.value.split("-").map(Number)
    if (parts.length !== 3) return
    var day = new Date(parts[0], parts[1] - 1, parts[2], 12, 0, 0)
    var zone = tz.value || getCurrentTimezone()
    var offsetMs = 0
    try {
      var here = day.toLocaleString("en-US", { timeZone: zone, hour12: false })
      var match = here.match(/(\d{4})-(\d{2})-(\d{2})/)
      if (!match) {
        var parsed = new Date(here)
        offsetMs = parsed.getTime() - day.getTime()
      }
    } catch (_) {}
    var localDay = new Date(day.getTime() + offsetMs).getDay()
    var button = panel.querySelector('[data-compose-weekday="' + localDay + '"]')
    if (button && button.getAttribute("data-active") !== "true") {
      button.setAttribute("data-active", "true")
      button.setAttribute("aria-pressed", "true")
      syncWeekdays(panel)
    }
  }

  // Called by scheduleCompose in app.js before submitting.
  window.validateScheduleRepeatForm = function (form) {
    var repeat = form.querySelector("[data-compose-schedule-repeat]")
    if (!repeat || repeat.value === "none") return null
    if (repeat.value === "weekly") {
      var selected = form.querySelectorAll("[data-compose-weekday][data-active='true']")
      if (!selected.length) return "Choose at least one weekday."
    }
    var end = form.querySelector("[data-compose-schedule-end]")
    var start = form.querySelector("[data-tui-calendar-hidden-input]")
    if (end && end.value && start && start.value && end.value < start.value) {
      return "The end date cannot be before the start date."
    }
    return null
  }

  window.initSchedulePopoverOnce = function () {
    document.querySelectorAll("[data-compose-schedule-panel]").forEach(initPopover)
  }
  document.addEventListener("click", function () { window.initSchedulePopoverOnce() }, true)

  // --- Scheduled folder plan panel -----------------------------------------

  function esc(value) {
    return String(value == null ? "" : value).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
    })
  }

  function fmtWhen(iso) {
    if (!iso) return ""
    var t = new Date(iso)
    if (isNaN(t.getTime())) return ""
    return t.toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })
  }

  function ruleSummary(plan) {
    var time = String(plan.hour).padStart(2, "0") + ":" + String(plan.minute).padStart(2, "0")
    if (plan.recurrence === "daily") return "Daily at " + time
    var days = (plan.weekdays || []).map(function (d) { return WEEKDAY_LABEL[d] }).join(", ")
    return "Weekly " + days + " at " + time
  }

  function api(url, options) {
    return fetch(url, Object.assign({ headers: { "Accept": "application/json" } }, options || {})).then(function (response) {
      return response.json().catch(function () { return {} }).then(function (data) {
        if (!response.ok) throw new Error(data.error || ("Request failed (" + response.status + ")"))
        return data
      })
    })
  }

  function statusBadge(plan) {
    if (plan.status === "paused") return '<span class="rounded-full bg-amber-500/15 px-2 py-0.5 text-[10px] font-semibold text-amber-700 dark:text-amber-400">Paused</span>'
    if (plan.status === "ended") return '<span class="rounded-full bg-muted px-2 py-0.5 text-[10px] font-semibold text-muted-foreground">Ended</span>'
    return '<span class="rounded-full bg-emerald-500/15 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:text-emerald-400">Active</span>'
  }

  function skipLabel(instance) {
    if (instance.status !== "skipped") return ""
    if (instance.skip_reason === "dst_gap") return "skipped: clock gap"
    if (instance.skip_reason === "paused") return "skipped: while paused"
    return "skipped: service missed it"
  }

  function instanceRow(instance) {
    var state = instance.status
    if (state === "skipped") state = skipLabel(instance)
    var actions = ""
    if (instance.outgoing_send_id) {
      if (instance.status === "failed" || instance.status === "ambiguous") {
        actions += '<button type="button" class="rounded px-2 py-0.5 text-[11px] font-semibold text-primary hover:underline" data-schedule-instance-action="retry" data-send-id="' + esc(instance.outgoing_send_id) + '">Retry</button>'
        actions += '<button type="button" class="rounded px-2 py-0.5 text-[11px] text-muted-foreground hover:underline" data-schedule-instance-action="cancel" data-send-id="' + esc(instance.outgoing_send_id) + '">Cancel</button>'
      } else if (instance.status === "pending" || instance.status === "sending") {
        actions += '<button type="button" class="rounded px-2 py-0.5 text-[11px] text-muted-foreground hover:underline" data-schedule-instance-action="cancel" data-send-id="' + esc(instance.outgoing_send_id) + '">Cancel</button>'
      }
    }
    return '<tr class="border-t border-border/50 text-[11px]">' +
      '<td class="py-1 pr-2 text-muted-foreground whitespace-nowrap">' + esc(instance.local_date) + '</td>' +
      '<td class="py-1 pr-2"><span data-instance-status="' + esc(instance.status) + '">' + esc(state) + '</span>' +
        (instance.last_error ? '<span class="block text-[10px] text-red-600 dark:text-red-400">' + esc(instance.last_error) + '</span>' : "") + '</td>' +
      '<td class="py-1 text-right whitespace-nowrap">' + actions + '</td></tr>'
  }

  function planCard(plan) {
    var controls = []
    if (plan.status === "active") controls.push(["pause", "Pause"])
    if (plan.status === "paused") controls.push(["resume", "Resume"])
    if (plan.status !== "ended") {
      controls.push(["edit", "Edit content"])
      controls.push(["settings", "Schedule"])
      controls.push(["end", "End"])
    }
    var buttons = controls.map(function (c) {
      return '<button type="button" class="rounded px-2 py-1 text-[11px] font-semibold ' +
        (c[0] === "end" ? "text-red-600 dark:text-red-400 " : "text-foreground ") +
        'hover:bg-accent" data-schedule-action="' + c[0] + '" data-schedule-id="' + esc(plan.id) + '">' + c[1] + "</button>"
    }).join("")
    var next = plan.next_occurrence_at ? "Next: " + esc(fmtWhen(plan.next_occurrence_at)) : "No upcoming send"
    var unfinished = plan.unfinished_count ? ' · <span class="text-amber-600 dark:text-amber-400">' + plan.unfinished_count + " needs attention</span>" : ""
    return '<article class="rounded-lg border border-border bg-background p-3 shadow-sm" data-schedule-card="' + esc(plan.id) + '">' +
      '<div class="flex items-start justify-between gap-2">' +
        '<div class="min-w-0">' +
          '<div class="flex items-center gap-2">' +
            '<h3 class="truncate text-sm font-semibold">' + esc(plan.subject || "(no subject)") + "</h3>" +
            statusBadge(plan) +
          "</div>" +
          '<p class="mt-0.5 text-xs text-muted-foreground">' + esc(ruleSummary(plan)) + " · " + esc(plan.timezone.replace(/_/g, " ")) + "</p>" +
          '<p class="mt-0.5 text-[11px] text-muted-foreground">' + next + unfinished + "</p>" +
        "</div>" +
        '<div class="flex shrink-0 items-center gap-0.5">' + buttons + "</div>" +
      "</div>" +
      '<details class="mt-1" data-schedule-history><summary class="cursor-pointer text-[11px] font-medium text-muted-foreground hover:text-foreground">Results</summary>' +
        '<div class="mt-1" data-schedule-history-body><table class="w-full"></table></div>' +
      "</details>" +
    "</article>"
  }

  function loadPlanHistory(card, planID) {
    var body = card.querySelector("[data-schedule-history-body] table")
    if (!body || body.dataset.loaded) return
    api("/api/send-schedules/" + encodeURIComponent(planID) + "/instances").then(function (data) {
      body.dataset.loaded = "1"
      var instances = data.instances || []
      if (!instances.length) {
        body.innerHTML = '<tr><td class="py-1 text-[11px] text-muted-foreground">No occurrences yet.</td></tr>'
        return
      }
      body.innerHTML = instances.map(instanceRow).join("")
    }).catch(function (err) {
      body.innerHTML = '<tr><td class="py-1 text-[11px] text-red-600">' + esc(err.message) + "</td></tr>"
    })
  }

  function bindCard(panel, card) {
    var planID = card.getAttribute("data-schedule-card")
    card.addEventListener("click", function (event) {
      var actionButton = event.target.closest("[data-schedule-action]")
      var instanceButton = event.target.closest("[data-schedule-instance-action]")
      if (event.target.closest("summary")) {
        if (!card.querySelector("details").open) loadPlanHistory(card, planID)
        return
      }
      if (instanceButton) {
        instanceAction(instanceButton)
        return
      }
      if (!actionButton) return
      var action = actionButton.getAttribute("data-schedule-action")
      if (action === "edit") {
        openContentEditor(planID)
        return
      }
      if (action === "settings") {
        openSettingsEditor(planID, panel)
        return
      }
      planAction(planID, action)
    })
  }

  function planAction(planID, action) {
    return api("/api/send-schedules/" + encodeURIComponent(planID) + "/" + action, { method: "POST" })
      .then(function () { window.loadSendSchedulePanel() })
      .catch(function (err) { window.alert(err.message) })
  }

  function instanceAction(button) {
    var sendID = button.getAttribute("data-send-id")
    var action = button.getAttribute("data-schedule-instance-action")
    if (action === "retry" && !window.confirm("Send this message again? It may already have been delivered.")) return
    var body = null
    if (action === "retry") body = JSON.stringify({ confirm: true })
    fetch("/api/outgoing-sends/" + encodeURIComponent(sendID) + "/" + action, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Accept": "application/json" },
      body: body,
    }).then(function (response) {
      if (!response.ok) return response.json().then(function (d) { throw new Error(d.error || "Request failed") })
      window.loadSendSchedulePanel()
    }).catch(function (err) { window.alert(err.message) })
  }

  function closeOverlays() {
    document.querySelectorAll("[data-schedule-editor]").forEach(function (node) { node.remove() })
  }

  function openEditorShell(plan, title) {
    closeOverlays()
    var wrap = document.createElement("div")
    wrap.setAttribute("data-schedule-editor", "")
    wrap.className = "fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
    wrap.innerHTML =
      '<div class="w-full max-w-md rounded-lg border border-border bg-card p-4 shadow-lg" role="dialog" aria-modal="true" aria-label="' + esc(title) + '">' +
        '<div class="flex items-center justify-between"><h2 class="text-sm font-semibold">' + esc(title) + "</h2>" +
        '<button type="button" class="text-muted-foreground hover:text-foreground" data-schedule-editor-close>✕</button></div>' +
        '<form class="mt-3 space-y-3" data-schedule-editor-form></form></div>'
    document.body.appendChild(wrap)
    wrap.addEventListener("click", function (event) {
      if (event.target === wrap || event.target.closest("[data-schedule-editor-close]")) wrap.remove()
    })
    return wrap.querySelector("[data-schedule-editor-form]")
  }

  function openContentEditor(planID) {
    api("/api/send-schedules/" + encodeURIComponent(planID)).then(function (plan) {
      var form = openEditorShell(plan, "Edit recurring content")
      var attachments = (plan.attachments || []).map(function (a, index) {
        return '<label class="flex items-center gap-2 text-xs"><input type="checkbox" checked data-keep-attachment="' + index + '"/> ' + esc(a.filename) + "</label>"
      }).join("")
      form.innerHTML =
        '<input type="hidden" name="account_id" value="' + esc(plan.account_id) + '"/>' +
        row("To", '<input name="to" class="composer-input h-9 w-full rounded-md border border-input bg-background px-2" required value="' + esc((plan.to || []).map(function (a) { return a.name ? a.name + " <" + a.address + ">" : a.address }).join(", ")) + '"/>') +
        row("Subject", '<input name="subject" class="composer-input h-9 w-full rounded-md border border-input bg-background px-2" value="' + esc(plan.subject || "") + '"/>') +
        row("Message", '<textarea name="body" rows="6" class="composer-input w-full rounded-md border border-input bg-background px-2 py-1">' + esc(plan.text_body || "") + "</textarea>") +
        (attachments ? '<div class="space-y-1"><p class="text-xs font-medium text-muted-foreground">Attachments</p>' + attachments + "</div>" : "") +
        row("", '<input type="file" name="new_attachments" multiple class="text-xs"/>') +
        '<div class="flex justify-end gap-2"><button type="button" class="rounded-md px-3 py-1.5 text-xs" data-schedule-editor-close>Cancel</button>' +
        '<button type="submit" class="btn-skeuo rounded-md px-3 py-1.5 text-xs font-semibold">Save content</button></div>'
      form.addEventListener("submit", function (event) {
        event.preventDefault()
        submitContentForm(planID, form, new FormData(form))
      })
    }).catch(function (err) { window.alert(err.message) })
  }

  function row(label, control) {
    return '<label class="block space-y-1">' + (label ? '<span class="text-xs font-medium text-muted-foreground">' + label + "</span>" : "") + control + "</label>"
  }

  function submitContentForm(planID, form, data) {
    // Existing attachments are carried forward server-side; unchecked ones
    // are removed by index.
    var removed = []
    form.querySelectorAll("[data-keep-attachment]").forEach(function (box) {
      if (!box.checked) removed.push(box.getAttribute("data-keep-attachment"))
    })
    if (removed.length) data.set("schedule_attachment_remove", removed.join(","))
    fetch("/api/send-schedules/" + encodeURIComponent(planID) + "/content", { method: "POST", body: data })
      .then(function (response) {
        if (!response.ok) return response.json().then(function (d) { throw new Error(d.error || "Request failed") })
        closeOverlays()
        window.loadSendSchedulePanel()
      }).catch(function (err) { window.alert(err.message) })
  }

  function openSettingsEditor(planID) {
    api("/api/send-schedules/" + encodeURIComponent(planID)).then(function (plan) {
      var form = openEditorShell(plan, "Edit schedule")
      var weekdayButtons = [0, 1, 2, 3, 4, 5, 6].map(function (day) {
        var checked = (plan.weekdays || []).indexOf(day) >= 0
        return '<button type="button" class="flex h-8 w-8 items-center justify-center rounded-full border text-xs" data-settings-weekday="' + day + '" data-active="' + checked + '">' + WEEKDAY_LABEL[day].slice(0, 1) + "</button>"
      }).join("")
      form.innerHTML =
        '<input type="hidden" name="account_id" value="' + esc(plan.account_id) + '"/>' +
        row("Repeats", '<select name="schedule_repeat" class="h-9 w-full rounded-md border border-input bg-background px-2"><option value="daily"' + (plan.recurrence === "daily" ? " selected" : "") + ">Daily</option><option value=\"weekly\"" + (plan.recurrence === "weekly" ? " selected" : "") + ">Weekly</option></select>") +
        '<div class="space-y-1" data-settings-weekdays><span class="text-xs font-medium text-muted-foreground">Weekdays</span><div class="flex flex-wrap gap-1">' + weekdayButtons + "</div></div>" +
        row("Time", '<div class="flex gap-2"><input name="schedule_hour" type="number" min="0" max="23" value="' + plan.hour + '" class="h-9 w-16 rounded-md border border-input bg-background px-2"/><input name="schedule_minute" type="number" min="0" max="55" step="5" value="' + plan.minute + '" class="h-9 w-16 rounded-md border border-input bg-background px-2"/></div>') +
        row("Time zone", '<input name="schedule_timezone" class="h-9 w-full rounded-md border border-input bg-background px-2" value="' + esc(plan.timezone) + '"/>') +
        row("Start date", '<input type="date" name="schedule_start_date" class="h-9 w-full rounded-md border border-input bg-background px-2" value="' + esc(plan.start_date) + '"/>') +
        row("End date (optional)", '<input type="date" name="schedule_end_date" class="h-9 w-full rounded-md border border-input bg-background px-2" value="' + esc(plan.end_date || "") + '"/>') +
        '<input type="hidden" name="schedule_weekdays" value="' + (plan.weekdays || []).join(",") + '"/>' +
        '<div class="flex justify-end gap-2"><button type="button" class="rounded-md px-3 py-1.5 text-xs" data-schedule-editor-close>Cancel</button><button type="submit" class="btn-skeuo rounded-md px-3 py-1.5 text-xs font-semibold">Save</button></div>'
      var repeat = form.querySelector("[name=schedule_repeat]")
      var weekdayBox = form.querySelector("[data-settings-weekdays]")
      var sync = function () {
        var days = []
        form.querySelectorAll("[data-settings-weekday]").forEach(function (b) {
          if (b.getAttribute("data-active") === "true") days.push(b.getAttribute("data-settings-weekday"))
        })
        form.querySelector("[name=schedule_weekdays]").value = days.join(",")
      }
      weekdayBox.querySelectorAll("button").forEach(function (b) {
        b.addEventListener("click", function () {
          b.setAttribute("data-active", b.getAttribute("data-active") === "true" ? "false" : "true")
          sync()
        })
      })
      repeat.addEventListener("change", function () { weekdayBox.style.opacity = repeat.value === "weekly" ? "1" : "0.4" })
      weekdayBox.style.opacity = repeat.value === "weekly" ? "1" : "0.4"
      form.addEventListener("submit", function (event) {
        event.preventDefault()
        var data = new URLSearchParams()
        Array.from(form.elements).forEach(function (el) {
          if (!el.name) return
          data.append(el.name, el.value)
        })
        fetch("/api/send-schedules/" + encodeURIComponent(planID), { method: "POST", body: data })
          .then(function (response) {
            if (!response.ok) return response.json().then(function (d) { throw new Error(d.error || "Request failed") })
            closeOverlays()
            window.loadSendSchedulePanel()
          }).catch(function (err) { window.alert(err.message) })
      })
    }).catch(function (err) { window.alert(err.message) })
  }

  var panelLoadToken = 0
  window.loadSendSchedulePanel = function () {
    var panel = document.querySelector("[data-send-schedule-panel]")
    if (!panel) return
    var list = panel.querySelector("[data-send-schedule-list]")
    var empty = panel.querySelector("[data-send-schedule-empty]")
    var token = ++panelLoadToken
    api("/api/send-schedules").then(function (data) {
      if (token !== panelLoadToken) return
      var plans = data.schedules || []
      list.querySelectorAll("[data-schedule-card]").forEach(function (c) { c.remove() })
      if (!plans.length) {
        empty.hidden = false
        return
      }
      empty.hidden = true
      plans.forEach(function (plan) {
        var wrap = document.createElement("div")
        wrap.innerHTML = planCard(plan)
        var card = wrap.firstElementChild
        list.appendChild(card)
        bindCard(panel, card)
      })
    }).catch(function () { /* stale folder swap; ignored */ })
  }

  // Reload only when folder navigation replaces the panel node, never on
  // the card mutations this module itself performs.
  var observedPanelNode = null
  var panelCheckTimer = null
  function checkPanelNode() {
    var node = document.querySelector("[data-send-schedule-panel]")
    if (node !== observedPanelNode) {
      observedPanelNode = node
      window.loadSendSchedulePanel()
    }
  }
  var scheduleObserver = new MutationObserver(function () {
    clearTimeout(panelCheckTimer)
    panelCheckTimer = setTimeout(checkPanelNode, 150)
  })
  function observePanelRoot() {
    scheduleObserver.disconnect()
    scheduleObserver.observe(document.body, { childList: true, subtree: true })
    checkPanelNode()
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", observePanelRoot)
  } else {
    observePanelRoot()
  }
  window.addEventListener("load", checkPanelNode)
})()
