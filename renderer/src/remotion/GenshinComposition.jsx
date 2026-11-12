import {AbsoluteFill, Easing, interpolate, OffthreadVideo, staticFile, useCurrentFrame, useVideoConfig} from 'remotion';
import {
  emblemCenter,
  genshinTimeline,
  getLabelFontSize,
  getRarityPalette,
  getShakeOffset,
  getStarRevealTimes,
  getTierEffects,
  getWishClip,
  starColor,
} from './genshin-timeline.js';

const baseFont =
  'Quicksand, Inter, "Noto Sans", "Noto Sans Vietnamese", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';

const clamp = /** @type {const} */ ({extrapolateLeft: 'clamp', extrapolateRight: 'clamp'});

/**
 * @param {string} hex
 * @param {number} alpha
 */
const withAlpha = (hex, alpha) => {
  const value = Number.parseInt(hex.slice(1), 16);
  return `rgba(${(value >> 16) & 255}, ${(value >> 8) & 255}, ${value & 255}, ${alpha})`;
};

/**
 * @param {{size: number}} props
 */
const StarIcon = ({size}) => (
  <svg height={size} viewBox="0 0 24 24" width={size}>
    <path
      d="M12 1.6l3.1 6.6 7.2.9-5.3 5 1.4 7.1L12 17.7l-6.4 3.5 1.4-7.1-5.3-5 7.2-.9z"
      fill={starColor}
      stroke="#fff6cf"
      strokeLinejoin="round"
      strokeWidth={0.8}
    />
  </svg>
);

/**
 * @typedef {import('../schemas/gacha-request.js').GachaRenderRequest} GachaRenderRequest
 */

/**
 * Wish animation: the pre-rendered clip for the rarity (a meteor falling over
 * a night sea, then the 3D rank emblem reveal) with the white impact flash,
 * the impact shake, the label, and the stars drawn on top.
 *
 * @param {GachaRenderRequest} props
 */
export const GenshinComposition = (props) => {
  const frame = useCurrentFrame();
  const {fps, width, height} = useVideoConfig();
  const seconds = frame / fps;
  const palette = getRarityPalette(props.rarity);
  const fx = getTierEffects(props.rarity);
  const tl = genshinTimeline;

  const flashOpacity = interpolate(seconds, [tl.flashStart, tl.flashPeak, tl.flashHoldEnd, tl.flashEnd], [0, 1, 1, 0], {
    ...clamp,
    easing: Easing.inOut(Easing.quad),
  });
  const impactX = width * emblemCenter.x;
  const impactY = height * emblemCenter.y;
  const bloomRadius = interpolate(seconds, [tl.flashStart, tl.flashPeak], [0.1, 2.2], clamp) * width;

  const shake = getShakeOffset(seconds, tl.revealStart, fx.shake * height);
  // Zoom the reveal slightly so the shake never shows the frame edge.
  const clipScale = seconds >= tl.revealStart && fx.shake > 0 ? 1.04 : 1;

  const nameProgress = interpolate(seconds, [tl.nameIn, tl.nameSettled], [0, 1], {
    ...clamp,
    easing: Easing.out(Easing.cubic),
  });
  const starSize = height * 0.09;
  const starTimes = getStarRevealTimes(props.rarity);

  return (
    <AbsoluteFill style={{background: '#05060f', fontFamily: baseFont, overflow: 'hidden'}}>
      <AbsoluteFill style={{transform: `translate(${shake.x}px, ${shake.y}px) scale(${clipScale})`}}>
        <OffthreadVideo muted src={staticFile(getWishClip(props.rarity))} style={{height: '100%', width: '100%'}} />
      </AbsoluteFill>

      <div
        style={{
          left: width * 0.07,
          opacity: nameProgress,
          position: 'absolute',
          top: height * 0.56,
          transform: `translateX(${(1 - nameProgress) * -width * 0.06}px)`,
          width: width * 0.44,
        }}
      >
        <div
          style={{
            color: '#ffffff',
            display: '-webkit-box',
            fontSize: getLabelFontSize(props.label, height),
            fontWeight: 800,
            lineHeight: 1.12,
            overflow: 'hidden',
            overflowWrap: 'anywhere',
            textShadow: `0 2px ${height * 0.012}px rgba(0, 0, 0, 0.8), 0 0 ${height * 0.035}px ${withAlpha(palette.glow, 0.8)}`,
            WebkitBoxOrient: 'vertical',
            WebkitLineClamp: 2,
          }}
        >
          {props.label}
        </div>
        <div style={{display: 'flex', gap: starSize * 0.12, marginTop: height * 0.025}}>
          {starTimes.map((time, index) => {
            const pop = interpolate(seconds, [time, time + 0.12, time + 0.24], [0, 1.6, 1], clamp);
            const flare = interpolate(seconds, [time, time + 0.3], [1, 0], clamp);
            const flareSize = starSize * 2.4 * fx.starFlare;
            return (
              <div
                key={`star-${index}`}
                style={{
                  height: starSize,
                  opacity: seconds >= time ? 1 : 0,
                  position: 'relative',
                  transform: `scale(${pop})`,
                  width: starSize,
                }}
              >
                <div
                  style={{
                    background: `radial-gradient(circle, ${withAlpha('#fff4c2', 0.9 * flare)} 0%, transparent 65%)`,
                    height: flareSize,
                    left: (starSize - flareSize) / 2,
                    position: 'absolute',
                    top: (starSize - flareSize) / 2,
                    width: flareSize,
                  }}
                />
                <StarIcon size={starSize} />
              </div>
            );
          })}
        </div>
      </div>

      {flashOpacity > 0 ? (
        <AbsoluteFill style={{pointerEvents: 'none'}}>
          <div
            style={{
              background: `radial-gradient(circle, #ffffff 0%, #ffffff 35%, ${withAlpha(palette.core, 0)} 70%)`,
              height: bloomRadius * 2,
              left: impactX - bloomRadius,
              opacity: Math.min(1, flashOpacity * 1.6),
              position: 'absolute',
              top: impactY - bloomRadius,
              width: bloomRadius * 2,
            }}
          />
          <AbsoluteFill style={{background: props.rarity === 5 ? '#fff6dc' : '#ffffff', opacity: flashOpacity}} />
        </AbsoluteFill>
      ) : null}
    </AbsoluteFill>
  );
};
