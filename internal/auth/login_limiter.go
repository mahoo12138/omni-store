package auth

import (
	"strings"
	"sync"
	"time"
)

const loginLimiterMaxTrackedKeys = 4096

type loginAttemptEvent struct {
	id uint64
	at time.Time
}

// LoginAttempt 是一次已原子预占限流额度的登录尝试。
type LoginAttempt struct {
	id       uint64
	ip       string
	username string
}

// LoginLimiter 按客户端 IP 和规范化用户名分别限制失败登录。
// IP 在尝试开始时硬性预占额度；用户名只在密码验证失败后计数，正确密码不会被他人锁死。
type LoginLimiter struct {
	mu             sync.Mutex
	window         time.Duration
	maxPerIP       int
	maxPerUsername int
	nextID         uint64
	byIP           map[string][]loginAttemptEvent
	byUsername     map[string][]loginAttemptEvent
	maxTrackedKeys int
	lastSweep      time.Time
	now            func() time.Time
}

// NewLoginLimiter 创建进程内滑动窗口登录限流器。阈值 <= 0 表示不限制该维度。
func NewLoginLimiter(window time.Duration, maxPerIP, maxPerUsername int) *LoginLimiter {
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &LoginLimiter{
		window:         window,
		maxPerIP:       maxPerIP,
		maxPerUsername: maxPerUsername,
		byIP:           make(map[string][]loginAttemptEvent),
		byUsername:     make(map[string][]loginAttemptEvent),
		maxTrackedKeys: loginLimiterMaxTrackedKeys,
		now:            time.Now,
	}
}

// Begin 原子检查并预占一次 IP 登录额度。用户名限流必须在密码验证失败后由 Failure 判定。
func (l *LoginLimiter) Begin(ip, username string) (*LoginAttempt, time.Duration, bool) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	// 用户名在当前数据模型中区分大小写，限流键必须保持同一语义。
	username = strings.TrimSpace(username)
	if username == "" {
		username = "unknown"
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.maintain(now)
	cutoff := now.Add(-l.window)
	ipEvents := pruneLoginEvents(l.byIP[ip], cutoff)
	l.storeEvents(l.byIP, ip, ipEvents)

	if l.maxPerIP > 0 && len(ipEvents) >= l.maxPerIP {
		return nil, retryDuration(now, ipEvents[0].at, l.window), false
	}

	l.nextID++
	attempt := &LoginAttempt{id: l.nextID, ip: ip, username: username}
	if l.maxPerIP > 0 {
		if _, exists := l.byIP[ip]; !exists && l.maxTrackedKeys > 0 && len(l.byIP) >= l.maxTrackedKeys {
			l.sweep(l.byIP, cutoff)
			if len(l.byIP) >= l.maxTrackedKeys {
				if !l.evictNonLimitedIP(l.byIP) {
					return nil, earliestRetry(l.byIP, now, l.window), false
				}
			}
		}
		l.byIP[ip] = append(ipEvents, loginAttemptEvent{id: attempt.id, at: now})
	}
	return attempt, 0, true
}

// Failure 保留 Begin 预占的 IP 失败，并在凭据验证失败后记录用户名维度。
// 超过用户名阈值的错误请求返回 allowed=false，但同用户名的正确密码仍可进入 Success。
// 当跟踪表已满且全为未过期失败记录时，绝不驱逐既有账号的失败计数（防用户名喷洒冲刷），
// 直接对新用户名 fail-closed 拒绝。
func (l *LoginLimiter) Failure(attempt *LoginAttempt) (time.Duration, bool) {
	if attempt == nil {
		return 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxPerUsername <= 0 {
		return 0, true
	}

	now := l.now()
	l.maintain(now)
	cutoff := now.Add(-l.window)
	events := pruneLoginEvents(l.byUsername[attempt.username], cutoff)
	allowed := len(events) < l.maxPerUsername
	retryAfter := time.Duration(0)
	if !allowed {
		retryAfter = retryDuration(now, events[0].at, l.window)
		l.storeEvents(l.byUsername, attempt.username, events)
		return retryAfter, false
	}
	if _, exists := l.byUsername[attempt.username]; !exists && l.maxTrackedKeys > 0 && len(l.byUsername) >= l.maxTrackedKeys {
		l.sweep(l.byUsername, cutoff)
		if len(l.byUsername) >= l.maxTrackedKeys {
			// 表中全部条目仍处于活跃失败窗口内：保留既有受害账号计数，对未知新键直接限流。
			return earliestRetry(l.byUsername, now, l.window), false
		}
	}
	l.byUsername[attempt.username] = append(events, loginAttemptEvent{id: attempt.id, at: now})
	return 0, true
}

// Success 释放本次 IP 额度，并清除该用户名的历史失败计数。
func (l *LoginLimiter) Success(attempt *LoginAttempt) {
	if attempt == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.storeEvents(l.byIP, attempt.ip, removeLoginEvent(l.byIP[attempt.ip], attempt.id))
	delete(l.byUsername, attempt.username)
}

// Cancel 在数据库等内部错误时释放本次预占，不把服务故障算作认证失败。
func (l *LoginLimiter) Cancel(attempt *LoginAttempt) {
	if attempt == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.storeEvents(l.byIP, attempt.ip, removeLoginEvent(l.byIP[attempt.ip], attempt.id))
}

func (l *LoginLimiter) maintain(now time.Time) {
	interval := time.Minute
	if l.window < interval {
		interval = l.window
	}
	if !l.lastSweep.IsZero() && now.Before(l.lastSweep.Add(interval)) {
		return
	}
	cutoff := now.Add(-l.window)
	l.sweep(l.byIP, cutoff)
	l.sweep(l.byUsername, cutoff)
	l.lastSweep = now
}

func (l *LoginLimiter) sweep(target map[string][]loginAttemptEvent, cutoff time.Time) {
	for key, events := range target {
		l.storeEvents(target, key, pruneLoginEvents(events, cutoff))
	}
}

// evictNonLimitedIP 仅在 IP 表满且无过期项时驱逐一个尚未触发限流且失败次数最少、最久未活跃的 IP，
// 绝不驱逐已经达到 maxPerIP 的限流 IP，防止攻击者通过喷洒新 IP 洗白被封禁来源。
func (l *LoginLimiter) evictNonLimitedIP(target map[string][]loginAttemptEvent) bool {
	victimKey := ""
	victimCount := 0
	var victimLast time.Time
	for key, events := range target {
		if len(events) == 0 {
			delete(target, key)
			return true
		}
		if l.maxPerIP > 0 && len(events) >= l.maxPerIP {
			continue
		}
		lastAt := events[len(events)-1].at
		if victimKey == "" || len(events) < victimCount || (len(events) == victimCount && lastAt.Before(victimLast)) {
			victimKey = key
			victimCount = len(events)
			victimLast = lastAt
		}
	}
	if victimKey == "" {
		return false
	}
	delete(target, victimKey)
	return true
}

func earliestRetry(target map[string][]loginAttemptEvent, now time.Time, window time.Duration) time.Duration {
	var earliest time.Time
	for _, events := range target {
		if len(events) == 0 {
			continue
		}
		if earliest.IsZero() || events[0].at.Before(earliest) {
			earliest = events[0].at
		}
	}
	if earliest.IsZero() {
		return window
	}
	return retryDuration(now, earliest, window)
}

func (l *LoginLimiter) storeEvents(target map[string][]loginAttemptEvent, key string, events []loginAttemptEvent) {
	if len(events) == 0 {
		delete(target, key)
		return
	}
	target[key] = events
}

func pruneLoginEvents(events []loginAttemptEvent, cutoff time.Time) []loginAttemptEvent {
	first := 0
	for first < len(events) && !events[first].at.After(cutoff) {
		first++
	}
	if first == len(events) {
		return nil
	}
	return events[first:]
}

func removeLoginEvent(events []loginAttemptEvent, id uint64) []loginAttemptEvent {
	for index, event := range events {
		if event.id == id {
			return append(events[:index], events[index+1:]...)
		}
	}
	return events
}

func retryDuration(now, oldest time.Time, window time.Duration) time.Duration {
	retry := oldest.Add(window).Sub(now)
	if retry <= 0 {
		return time.Nanosecond
	}
	return retry
}
