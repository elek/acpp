package net.anzix.acpp.ui.util

import java.util.Locale

/** Compact token count, e.g. 103800 -> "103.8k", 1_200_000 -> "1.2M". */
fun formatTokens(v: Long): String = when {
    v <= 0 -> "0"
    v >= 1_000_000 -> String.format(Locale.US, "%.1fM", v / 1_000_000.0)
    v >= 1_000 -> String.format(Locale.US, "%.1fk", v / 1_000.0)
    else -> v.toString()
}

/** Formats a USD cost, e.g. 1.27 -> "$1.27". */
fun formatCost(usd: Double?): String =
    if (usd == null) "" else String.format(Locale.US, "$%.2f", usd)
