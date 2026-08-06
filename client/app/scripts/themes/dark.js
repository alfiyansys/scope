import theme from 'weaveworks-ui-components/lib/theme';
import { transparentize } from 'polished';
import defaultTheme from './default';

const darkBodyBackground = 'hsl(240, 15%, 10%)';
const darkPanelBackground = 'hsl(240, 15%, 15%)';

const darkTheme = {
  ...defaultTheme,

  /* dark mode overrides */
  backgroundColor: 'hsl(240, 15%, 13%)',
  backgroundDarkColor: 'hsl(240, 15%, 8%)',
  backgroundDarkerColor: 'hsl(240, 15%, 6%)',
  backgroundDarkerSecondaryColor: 'hsl(240, 15%, 13%)',
  bodyBackgroundColor: darkBodyBackground,
  borderLightColor: theme.colors.purple700,
  edgeColor: theme.colors.purple300,
  labelBackgroundColor: transparentize(0.15, darkBodyBackground),
  panelBackgroundColor: darkPanelBackground,
  textColor: theme.colors.gray50,
  textDarkerColor: theme.colors.white,
  textSecondaryColor: theme.colors.purple200,
  textTertiaryColor: theme.colors.purple300,
};

export default darkTheme;
