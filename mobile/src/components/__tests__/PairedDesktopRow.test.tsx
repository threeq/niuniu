import { render, fireEvent } from '@testing-library/react-native';
import { PairedDesktopRow } from '../PairedDesktopRow';
import type { PairedDesktop } from '../../stores/pairedDesktopsStore';

const sample: PairedDesktop = {
  desktopId: 'desk-abcdef0123456789',
  desktopName: 'My MacBook',
  xpub: 'xpub',
  signPub: 'signpub',
  relayDeviceToken: 'token',
  pairedAt: new Date('2026-04-01T12:00:00Z').toISOString(),
};

describe('PairedDesktopRow', () => {
  it('renders desktop name and a 12-char short id', () => {
    const { getByText } = render(
      <PairedDesktopRow desktop={sample} onPress={jest.fn()} />,
    );
    expect(getByText('My MacBook')).toBeTruthy();
    // The short id is the first 12 chars of desktopId followed by "...".
    expect(getByText('desk-abcdef0...')).toBeTruthy();
  });

  it('does not render the Active badge by default', () => {
    const { queryByTestId } = render(
      <PairedDesktopRow desktop={sample} onPress={jest.fn()} />,
    );
    expect(queryByTestId('paired-desktop-row-active-badge')).toBeNull();
  });

  it('renders the Active badge when isActive=true', () => {
    const { getByTestId } = render(
      <PairedDesktopRow desktop={sample} isActive onPress={jest.fn()} />,
    );
    expect(getByTestId('paired-desktop-row-active-badge')).toBeTruthy();
  });

  it('fires onPress when the row is pressed', () => {
    const onPress = jest.fn();
    const { getByTestId } = render(
      <PairedDesktopRow desktop={sample} onPress={onPress} />,
    );
    fireEvent.press(getByTestId('paired-desktop-row'));
    expect(onPress).toHaveBeenCalledTimes(1);
  });

  it('fires onLongPress when the row is long-pressed', () => {
    const onLongPress = jest.fn();
    const { getByTestId } = render(
      <PairedDesktopRow
        desktop={sample}
        onPress={jest.fn()}
        onLongPress={onLongPress}
      />,
    );
    fireEvent(getByTestId('paired-desktop-row'), 'longPress');
    expect(onLongPress).toHaveBeenCalledTimes(1);
  });

  it('renders without pairedAt without crashing', () => {
    const { getByText } = render(
      <PairedDesktopRow
        desktop={{ ...sample, pairedAt: '' }}
        onPress={jest.fn()}
      />,
    );
    expect(getByText('My MacBook')).toBeTruthy();
  });
});
