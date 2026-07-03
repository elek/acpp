package net.anzix.acpp.ui.util

import java.time.Duration
import java.time.Instant

/** Formats an RFC3339 timestamp as a compact relative time (e.g. "5m", "2h", "3d"). */
object RelativeTime {
    fun format(iso: String, now: Instant = Instant.now()): String {
        val then = runCatching { Instant.parse(iso) }.getOrNull() ?: return ""
        val d = Duration.between(then, now)
        val secs = d.seconds
        return when {
            secs < 0 -> "now"
            secs < 60 -> "now"
            secs < 3600 -> "${secs / 60}m"
            secs < 86_400 -> "${secs / 3600}h"
            secs < 604_800 -> "${secs / 86_400}d"
            else -> "${secs / 604_800}w"
        }
    }
}
