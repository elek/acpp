package net.anzix.acpp.ui.components

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import net.anzix.acpp.domain.model.ConversationStatus
import net.anzix.acpp.ui.theme.ErrorContainer
import net.anzix.acpp.ui.theme.OnErrorContainer
import net.anzix.acpp.ui.theme.OnSurfaceVariant
import net.anzix.acpp.ui.theme.SecondaryContainer
import net.anzix.acpp.ui.theme.StatusGreenBg
import net.anzix.acpp.ui.theme.StatusGreenDot
import net.anzix.acpp.ui.theme.StatusGreenText
import net.anzix.acpp.ui.theme.SurfaceContainerHigh

private data class StatusVisuals(
    val bg: Color,
    val fg: Color,
    val dot: Color,
    val label: String,
)

private fun visualsFor(status: ConversationStatus): StatusVisuals = when (status) {
    ConversationStatus.RUNNING -> StatusVisuals(StatusGreenBg, StatusGreenText, StatusGreenDot, "Running")
    ConversationStatus.DONE -> StatusVisuals(SecondaryContainer, Color(0xFF1D192B), Color(0xFF1D192B), "Done")
    ConversationStatus.ERROR -> StatusVisuals(ErrorContainer, OnErrorContainer, OnErrorContainer, "Error")
    ConversationStatus.IDLE -> StatusVisuals(SurfaceContainerHigh, OnSurfaceVariant, OnSurfaceVariant, "Idle")
}

@Composable
fun StatusChip(status: ConversationStatus, modifier: Modifier = Modifier) {
    val v = visualsFor(status)

    val dotAlpha = if (status == ConversationStatus.RUNNING) {
        val transition = rememberInfiniteTransition(label = "pulse")
        val a by transition.animateFloat(
            initialValue = 1f,
            targetValue = 0.3f,
            animationSpec = infiniteRepeatable(tween(700), RepeatMode.Reverse),
            label = "pulseAlpha",
        )
        a
    } else {
        1f
    }

    Row(
        modifier = modifier
            .clip(RoundedCornerShape(10.dp))
            .background(v.bg)
            .padding(horizontal = 10.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        androidx.compose.foundation.layout.Box(
            modifier = Modifier
                .size(8.dp)
                .alpha(dotAlpha)
                .clip(CircleShape)
                .background(v.dot),
        )
        Text(
            text = v.label,
            color = v.fg,
            style = MaterialTheme.typography.labelMedium,
        )
    }
}
