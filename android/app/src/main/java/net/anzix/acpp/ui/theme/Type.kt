package net.anzix.acpp.ui.theme

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

// UI font is the platform default (Roboto on Android). The "mono" role (paths,
// branches, tokens, hashes, costs) uses the bundled monospace family. JetBrains Mono
// can be swapped in later by bundling its .ttf and pointing MonoFamily at it.
val MonoFamily = FontFamily.Monospace

val AcppTypography = Typography()

// Large title used by the Projects top bar (32sp per handoff).
val LargeTitle = TextStyle(
    fontFamily = FontFamily.Default,
    fontWeight = FontWeight.SemiBold,
    fontSize = 32.sp,
    lineHeight = 40.sp,
)

// Inline / code text style.
val MonoCode = TextStyle(
    fontFamily = MonoFamily,
    fontSize = 13.sp,
)
