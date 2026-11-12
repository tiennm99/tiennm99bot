import {Composition} from 'remotion';
import {WheelComposition} from './WheelComposition.jsx';

/** @type {import('../schemas/wheel-request.js').WheelRenderRequest} */
const defaultProps = {
  options: ['alice', 'bob', 'carol', 'dave', 'erin', 'frank', 'grace', 'heidi'],
  winnerIndex: 2,
  durationMs: 6500,
  holdMs: 1200,
  fps: 15,
  size: 512,
  theme: 'classic',
};

/**
 * @param {{durationMs: number, holdMs: number, fps: number, size: number}} props
 */
export const getCompositionMetadata = (props) => {
  const fps = props.fps || defaultProps.fps;
  const totalMs = (props.durationMs || defaultProps.durationMs) + (props.holdMs || defaultProps.holdMs);
  const durationInFrames = Math.max(1, Math.ceil((totalMs / 1000) * fps));
  const size = props.size || defaultProps.size;

  return {
    durationInFrames,
    fps,
    height: size,
    width: size,
  };
};

export const RemotionRoot = () => (
  <>
    <Composition
      calculateMetadata={({props}) => getCompositionMetadata(props)}
      component={WheelComposition}
      defaultProps={defaultProps}
      durationInFrames={Math.ceil(((defaultProps.durationMs + defaultProps.holdMs) / 1000) * defaultProps.fps)}
      fps={defaultProps.fps}
      height={defaultProps.size}
      id="WheelGif"
      width={defaultProps.size}
    />
  </>
);
